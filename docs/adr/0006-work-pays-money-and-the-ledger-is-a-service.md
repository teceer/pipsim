# 6. Work pays money, and needs belong to the core

Status: proposed

## Context

Work currently pays in food. `farm.go:58` hands a worker `foodPerTick = 5` and
`shifts.ex:62` hands one `+3`, and both files say the same thing in a comment:
the food is *conjured*. Nothing produced it, nothing holds it, no stock is drawn
down. The grain the farm reports as `ResourceProduced` goes nowhere — it is a
number in a Kafka topic that no consumer turns back into a meal.

Three problems follow, and only the third is about gameplay.

**Every workplace is forced to have an opinion about hunger.**
`WorkResponse.need_deltas` is the only channel a building has for rewarding a
shift, so a building cannot pay without deciding what food is worth. The farm
sets `needFood: +5`, the tavern `+3`, the workshop stub `-2`. That is one domain
rule — *what feeds a pip* — implemented in Go, Elixir and TypeScript, which is
what rule 4 forbids. The duplication is not accidental and cannot be fixed by
sharing a library: `shifts.ex:5` refuses to share one on purpose, and is right
to. The fix has to be in the contract.

**The rule is already implemented twice, and the second copy is in the core.**
`lib.rs:441` drains 2 food per tick for `Activity::Working` against 1 for
anything else — the core already knows work is tiring. Meanwhile `farm.go:191`
returns `needRest: -1` to express the same fact. Both are correct, neither knows
about the other, and the pip's actual metabolism is whatever the two happen to
sum to.

**This has already produced a bug.** The tavern shipped with `food_per_tick =
-1` and killed everyone it employed: a working pip loses 2 food a tick to the
world, so a shift there was −3 against the farm's +3. It was found in the
cluster by watching tavern staff fall from 990 food to 221 (`shifts.ex:52`). The
tavern was not wrong about taverns. It was wrong about a number it should never
have been allowed to name.

**There is no scarcity, so there are no decisions.** A pip has exactly one lever
on its own life — take a job or do not — and afterwards it is moved. `social`
decays and only the tavern restores it, so a hungry pip and a lonely pip face
the same non-choice: whichever building will have them.

The instinct behind the current shape is right: a building owns its own rules
and does not know other buildings exist. The mistake is that *reward* and
*nutrition* were collapsed into one field, which forces every building to
describe the world's metabolism in order to describe itself.

## Decision

### 1. Workplaces declare what they offer and require. They never name a need.

`need_deltas` leaves the contract. A workplace describes itself in the only
vocabulary it is qualified for:

| A workplace declares | Example |
|---|---|
| what it produces | farm → `GRAIN` |
| what it consumes | workshop → `GRAIN` |
| what it sells, and at what price | tavern → `ALE` at 4 |
| what it pays for a shift | farm → wage 6 per tick |
| how demanding the work is | tavern → effort 2 |

**What a resource does to a pip is a property of the resource, owned by
`sim-core` and written down once** — a table from `ResourceKind` to need
effects, in Rust, deterministic, inside `state_hash`. `ALE` restores social
everywhere in the world, not "in the tavern", because that is what ale is.

This is not a refinement of the current split. It inverts it. Today a building
says *what happens to the pip*; afterwards it says *what exists and what it
costs*, and the core says what that means for a body. A building cannot starve
its workers by getting a constant wrong, because it can no longer express the
constant. The tavern bug becomes unrepresentable rather than fixed.

`effort` deserves a note, because it is the tempting place to smuggle the old
design back in. It is a scalar the building declares about the work, not a need
delta — the core decides what effort costs, exactly as `lib.rs:441` already
does per activity. If it ever grows a per-need shape, it has become
`need_deltas` again.

### 2. Money is the only generic edge

Work pays a wage; goods have prices. That is the whole coupling between
services, and it carries no domain semantics — which is precisely why it can be
shared without coupling anything. The farm knows it produces grain and pays 6.
The tavern knows it sells ale at 4. Neither knows what hunger is, neither knows
the other exists.

Barter would need an exchange rate per pair of building types, which is O(n²) in
the thing this ADR exists to keep at O(n). That is the same reason money was
invented.

### 3. Everything is a transfer. There is no cash.

An earlier draft of this ADR split money in two: bank accounts for workplaces,
physical coins in a pip's pocket. That was wrong, and simpler is also more
correct here. Every actor — pip, workplace, treasury — has exactly one account
in the bank, and every movement of money is a transfer between two of them.
No second pool, no reconciliation between two kinds of money, no invariant
tying them together.

What does *not* go away is that the core must be able to answer "can this pip
afford it" inside a tick, and rule 3 forbids it from asking anyone. So:

**The bank owns balances. The core holds a replica of the balances it needs to
make decisions with, updated only by intents.**

This is the pattern ADR 0004 already established for `max_workers`: the number
has exactly one owner, the gateway copies it into the core with an intent, and
the core enforces it locally. Balances work the same way, with one addition —
here the copy flows both ways, because spending originates in the core.

The direction of error matters and is deliberately safe. Every change to a pip's
balance passes through the core as an intent, so the core is never *behind* on
spending — it can only be behind on income that has not yet been applied. A pip
may briefly believe it is poorer than the ledger says. It can never spend money
it does not have. The bank is authoritative for history and for solvency; the
core is authoritative for the moment of purchase.

### 4. The bank is a service with a double-entry ledger

`services/bank`, its own Postgres schema, holding accounts and a journal in
which every transfer is two rows summing to zero. Never a mutable `balance`
column as the source of truth — a balance is a fold over the journal, cached and
re-derivable. Same reasoning as the event log: when the economy does something
inexplicable, the answer must be reconstructable rather than guessed at.

Account ids are namespaced by holder kind, so `workplace:3` and `pip:412` cannot
collide.

**Money supply is closed by default.** Every transfer is a move, never a
creation; issuance is a distinct privileged operation from the treasury and is
visible as such in the journal. That buys the strongest invariant in the
project: *the sum of all accounts is constant except at an explicit issuance*.
One property test in the bank, and one e2e assertion that the core's replicas
agree with the ledger.

**Every transfer carries an idempotency key**, `(payer, payee, tick, kind)`.
Not defensive habit: RabbitMQ redelivers, `Fleet.Run` retries a cut-short cycle
by design, and both `farm.go:Work` and `workplace.ex:83` already carry
hand-rolled guards against being paid twice in one tick — because it has
happened. With money the same bug mints currency instead of food, so the guard
moves into the ledger where it is enforced once for everyone.

### 5. Bus split

Consistent with ADR 0002:

- **Connect** — `Transfer`, `OpenAccount`, `GetBalance`. The caller waits,
  because "can payroll be met" has an answer a decision depends on.
- **Kafka** — `WagePaid`, `PurchaseMade`, `AccountOverdrawn`, `MoneyIssued`.
- **BullMQ** — interest, rent, scheduled payroll. The first genuinely natural
  use of delayed jobs here; today the queue holds timers, not obligations.

## Why not let the core ask the bank during a tick

It would remove the replica and make the bank unambiguously authoritative.

Rejected on rule 3, and on the two properties the README calls the point of the
project. A network call inside `step` destroys determinism outright. And the
WASM build has no bank to call, so client prediction would go blind exactly
where the interesting decisions are — `make parity` could no longer cover the
economy at all. Anything a pip decides on has to be in the core's state.

## Why not keep balances only in the bank and leave the core ignorant of money

The version where buying food is an RPC and the core never sees a balance.

It breaks replay's explanatory power. Rewinding to tick 4,700 would show *that*
everyone starved, not that wages had fallen below the price of bread, because
the cause lives in a Postgres row outside the event log. "You can rewind and
watch exactly why everyone starved" stops being true the moment the reason is
economic.

## Why not keep everything in the core and make the bank a read model

Determinism would be perfect and the bank a projection off Kafka.

Rejected because it makes the core responsible for things it cannot represent. A
workplace running out of money, borrowing, or going bankrupt is a decision with
history and policy behind it; the core is a fixed-size `Vec` of integers with no
clock. It would also put prices and wages in the core — the authority ADRs 0004
and 0005 spent their effort pushing *out* of it.

## Consequences

- **The contract change is a removal, and `buf breaking` will not allow one.**
  `WorkResponse.need_deltas` is field 2 and must be `reserved`, not deleted, per
  the rule that contracts change by adding. The same applies to
  `ApplyNeedsIntent` in `sim.proto` once nothing produces it.
- **`sim-core` gains a resource table, and it is a domain decision in the
  core.** This is new: until now the core owned physics and the services owned
  economics. The line moves — the core owns *metabolism*, which is neither. It
  is the right home for it, because metabolism is a property of pips and the
  core is the only thing that owns pips.
- **A pip gets its first real decision.** Food or entertainment, from one
  budget, is a preference — and preferences are what make `social` a motive
  rather than a stat. Which need a pip services when it cannot afford both is
  now a rule someone has to write, and it belongs to the core.
- **New failure modes, all legible in replay.** A pip can die with money and
  nothing for sale, or die employed because the wage is below the price of
  bread. Neither is expressible today.
- **`state_hash` changes** — balances are mixed in, which is what puts the
  economy under the parity test. Every recorded hash fixture is invalidated
  once.
- **Payroll is a batch, not a call per pip.** At 24 workers on a one-second
  cycle, per-pip synchronous transfers would put the ledger in the tick budget.
  One `Transfer` per workplace per cycle, one intent carrying the credits back.
- **The bank must not be a single point of failure for living.** If it is down,
  wages stop; pips keep walking, keep spending what the core says they have, and
  keep dying normally. The gateway skips payroll rather than stalling the cycle
  — the posture `farm.go:Work` already takes toward a store blip, where
  `Unavailable` is deliberately not `shift_should_end`.
- **Prices have one owner: the selling workplace.** The bank moves money and
  reports whether the payer had it. If `bank` grows an `if kind == "ale"`, the
  boundary has leaked in the way `economy.go`'s package comment warns about.
- **The conformance suite grows a money chapter.** "A workplace pays what it
  declares, does not pay twice for one tick, and charges the price it
  advertises" is checkable against any implementation — which is what keeps the
  polyglot claim honest as the contract widens.
- **Bankruptcy is deliberately undefined.** This ADR establishes the ledger, not
  insolvency policy. An overdrawn workplace emits a fact and keeps operating
  until a later decision says otherwise.

## Next steps

1. **`proto/pips/bank/v1/bank.proto`** — `OpenAccount`, `Transfer`, `GetBalance`,
   `BatchTransfer`. Contract first, per rule 1.
2. **The resource table in `sim-core`** — `ResourceKind` → need effects, plus
   `Consume`. Purely additive: nothing calls it yet, and the existing
   `need_deltas` path still works.
3. **Balances in `sim-core`** — beside `needs`, in `state_hash`, moved only by
   `TransferIntent`.
4. **`services/bank`** — journal, double-entry, idempotency keys, closed-supply
   property test. Correct before anything depends on it.
5. **`Describe` gains `sells`, `wage`, `effort`; `WorkResponse` gains `wage`.**
   Additive. Payroll starts flowing while `need_deltas` still conjures food, so
   wages can be tuned against a living world.
6. **`Buy` in `workplace/v1`** — the tavern first, since it already sells
   something. Food follows.
7. **Reserve `need_deltas` and delete `foodPerTick` everywhere.** Until this
   step the ADR has not paid for itself.
