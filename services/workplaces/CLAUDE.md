# workplaces

Every workplace implements exactly one contract: `pips.workplace.v1.WorkplaceService`.

This directory is where the polyglot goal is exercised on purpose. Workplaces are
small, isolated and identically shaped, so rotating languages costs little and
blowing one up does not take the game down.

| Workplace | Language | Produces | Status |
|---|---|---|---|
| `farm` | Go | grain | running |
| `tavern` | Elixir | ale, restores the social need | running |
| `workshop` | TypeScript | tools | not built yet |

## The claim, and how it is checked

`services/workplaces/conformance` is a Go test suite that drives *any* running
workplace through the contract using the same Connect client the gateway uses:

```bash
make -C services/workplaces/tavern run &
WORKPLACE_ADDR=localhost:8090 go test ./services/workplaces/conformance -v
```

It is skipped without `WORKPLACE_ADDR`, so `make test` stays cluster-free.

Run it against a new workplace before believing it works. The first run against
the Elixir tavern failed on a response content type of
`application/proto; charset=utf-8` — connect-go compares that string exactly.
The tavern's own tests all passed, because both sides of them shared the bug.
That is the class of thing only a cross-implementation check finds.

## The rule that keeps this working

**If you want to extend `workplace.proto` for one specific building, stop.**
That is the signal the abstraction is wrong, and the fix is almost always to
express the building's specialness through `Describe` metadata or through the
resources it consumes and produces — not through a new RPC that only one
implementation answers.

The payoff of holding that line: adding a new building type is an hour of work
in any language and touches neither sim-core nor the gateway.

## What every workplace must do

- implement all five RPCs (`Describe`, `CanEmploy`, `StartShift`, `Work`, `EndShift`)
- own its own Postgres schema; never read another service's tables
- publish `ResourceProduced` facts through the gateway, not directly
- expose `/healthz`, JSON logs, and OTel spans named `pipsim.<kind>.<operation>`
- provide the standard Make targets: `run` `test` `lint` `build` `gen`

## What no workplace may do

- know that another workplace exists
- hold pip state — pips belong to sim-core; a workplace knows only who is on
  shift right now
- decide anything about needs beyond returning `need_deltas` from `Work`

## What the farm established

It is the template, and two of its choices are worth copying:

**Shifts are held on a lease.** A shift nobody asks to `Work` for fifteen
seconds expires by itself. This is not defensive coding — it fixes a failure
seen in the cluster, where sim-core restarted with a fresh world and the farm
went on holding every position for pips that no longer existed, so nobody could
be hired again. A lease beats a reconciliation RPC because a workplace should
not have to ask anyone who still exists.

**Shift state belongs in a store, not in the process.** `farm.Service` holds no
map; it holds a `Store` (`Claim`, `Touch`, `Release`, `Count`). In memory it is
correct at one replica; in Redis it is correct at any number. Copy the interface
rather than the map — running two replicas with per-process shifts produced
three parties with three different headcounts, and the shape of that bug is
identical in every workplace.

Whatever backs it, `Claim` must be atomic across reap-check-insert. Splitting it
into "is there room" and "take the room" reintroduces the race the store exists
to remove.

**`Work` pays for elapsed ticks, not per call.** The driver batches — it calls
`Work` once a second while the world ticks ten times. Flat per-call amounts made
employment a *slower death* than idling, because a working pip drains food every
tick and was paid once. Scaling by `tick - lastWorkTick` makes the contract
independent of whatever cadence the caller picks, and the amount is capped so a
stalled driver cannot hand out a windfall.

## What the tavern established

**A workplace does not need a gRPC library.** The gateway calls workplaces over
the *Connect* protocol, and unary Connect is an ordinary HTTP POST to
`/<package>.<Service>/<Method>` carrying a bare protobuf body — no framing, no
HTTP/2 requirement, no trailers. The tavern serves it with Plug and Bandit in
about a hundred lines (`lib/tavern/connect.ex`). If a language you want to use
has no gRPC story, that is not a reason to skip it.

Two details that are easy to get wrong and hard to notice:

- the response content type must be exactly `application/proto`, with no
  `charset` parameter;
- accepting `application/json` too costs nothing and makes `curl` a debugger.

**Rules are reimplemented, not shared.** The tavern's lease, its elapsed-tick
pricing and its atomic claim are written again in Elixir rather than extracted
into a library. A workplace is a service; two services sharing an
implementation is how "no rule in two languages" quietly becomes "one rule, two
deployments, one of them stale". What is shared is the contract.

## Adding a new one

1. Copy the closest existing workplace — `farm` for Go, `tavern` for anything
   that has to hand-roll the transport.
2. Implement the five RPCs.
3. Run the conformance suite against it.
4. Add it to `services` in the root `Makefile` and to the loop in `Tiltfile`.
5. Add a Helm chart under `infra/helm/`, and its address to
   `workplaceAddrs` in the gateway's values.

No change to `proto/`, sim-core or the gateway should be needed. If one is, the
contract is leaking.
