# 5. A workplace service is a building type, not a building

Status: proposed

## Context

Today a building instance *is* a deployment. `infra/helm/farm/templates/deployment.yaml:30`
carries exactly one `WORKPLACE_ID`, one `WORKPLACE_X`, one `WORKPLACE_Y`. Two farms
means two Helm releases; a pip building a farm at runtime means `helm install` from a
browser.

Four consequences follow, and they are the reason to write this down:

- **Cost.** `values.yaml` requests `50m`/`64Mi` at `replicaCount: 2`, so one building
  costs 100m CPU and 128Mi before it does anything. A hundred buildings is 10 cores and
  12.8 GiB, for what is in essence the tuple `(id, kind, position, capacity, occupants)`.
- **Discovery is a hand-maintained list.** `services/world-gateway/cmd/gateway/main.go:100`
  splits `WORKPLACE_ADDRS` out of the environment. There is no discovery of any kind, and
  the chart has already drifted — `infra/helm/world-gateway/templates/deployment.yaml:36`
  still sets the deprecated `FARM_ADDR`.
- **There is no lifecycle.** `RegisterWorkplace` has no inverse in `sim.proto`, and
  `WorkplaceDemolished` exists in `proto/pips/events/v1/events.proto:87` but is never
  emitted. A deleted deployment leaves its building on the map forever.
- **The gateway cannot actually scale.** `deployment.yaml:8` claims it does because it
  holds no world state, but two replicas would both run `Fleet.Run` and call `Work` for
  every employed pip — double pay. This is the same bug ADR-adjacent commit 2922b07 fixed
  on the farm side. `replicaCount: 1` is load-bearing and undocumented.

The instinct behind the current shape is sound: a building should be an independent unit
with its own logic, state, and failure. The mistake is choosing the pod as the granularity
for that unit, because two farms run the identical binary and differ by two numbers.
Nothing about distributed systems is learned from the second copy — the interesting axis
is farm ↔ tavern ↔ workshop, which is untouched by this ADR.

## Decision

**A workplace service owns a *kind* of building; instances are data.**

`workplace/v1` already anticipates this. Every RPC carries `workplace_id`
(`CanEmployRequest:2`, `WorkRequest:1`, `StartShiftRequest:1`) and the field is currently
decorative, because the answer is always about the one building the process was configured
with. The state layer is already there too: `services/workplaces/farm/internal/farm/store.go:150`
keys shifts as `pipsim:workplace:<id>:shifts`, so occupancy and capacity are already
per-instance. What is hardcoded is only the entry point.

`Describe` gains a sibling `List` that returns every instance a service hosts. Additive, so
`buf breaking` stays green.

**Buy the entity runtime rather than writing it.** Instances become Dapr virtual actors:
addressable by id, activated on demand, turn-based concurrency per id, pluggable state
store, timers and reminders included. That dissolves the four problems above at once —
discovery becomes an app-id, deactivation gives the missing lifecycle, and the two Lua
scripts in `store.go` become an actor state store on the Redis already in the stack.

**Shifts are a separate decision, deferred.** A shift is a *process* — it starts, runs N
ticks, can fail, needs cleaning up — and that is a Temporal workflow, not an actor. The
distinction is durable *state* (Dapr: the actor reactivates elsewhere, the in-flight call
is lost) versus durable *execution* (Temporal: another worker replays history and resumes
mid-function). Today the shift is a hand-rolled loop in `Fleet.Run` plus leases in Lua.
Revisit once buildings are actors; doing both at once is too many variables.

## Why not a pod per building, done properly

The honest version of the status quo is an **operator with a `Building` CRD** — a
controller reconciling custom resources into Deployments, which is how Agones runs game
servers. It is a real production pattern and teaches reconciliation loops, level-triggered
APIs, RBAC and finalizers.

Rejected on granularity, not on principle. Pod-per-entity earns its keep when the entity is
long-lived, compute-heavy and needs isolation. A pipsim building is long-lived and neither
of the other two. It also puts the gateway on the Kubernetes API, which breaks the rule that
every service is testable without a cluster, and caps the world at hundreds of buildings.

## Why not Kubernetes API discovery

Same objection, smaller blast radius: it needs RBAC, it couples the gateway to the platform,
and `make dev` on Compose stops resembling production. It also watches the wrong thing — pods
are replicas, not buildings, so the cardinality is wrong before the first query.

## Why not a supervision layer written in Elixir

Considered seriously, since `tavern` and `broadcast` are already on the BEAM and a
`GenServer` per building under a `DynamicSupervisor` gives franchise semantics for free:
identical code, isolated state, isolated failure, at ~2 KiB per unit instead of ~64 MiB.
Erlang's supervision tree is a reconciliation loop that predates Kubernetes by three decades.

Rejected because it is an implementation of what Dapr and Temporal sell. The default for this
repo is integrating production tooling; hand-rolling is reserved for parts of the stack we
deliberately want to study. Note the cost of that choice: neither Dapr nor Temporal has an
official Elixir SDK, so `tavern` needs hand-written glue against the actor HTTP contract.

Also rejected: **NIFs** (Rustler and friends) as the polyglot bridge. They are the fastest
option and they destroy the property being bought — native code shares the VM's address
space, so a segfault takes down every building at once.

## Consequences

- **Failure isolation stops being free.** Today pod-per-building means Kubernetes' unit of
  isolation accidentally matches the domain's. Once one process hosts forty farms it does
  not, and Dapr's turn-based actors have to supply it. Independent of Dapr, `farm` needs
  `connect.WithRecover()` on the handler (`services/workplaces/farm/cmd/farm/main.go:82` has
  no recovery of any kind), a `recover` in every spawned goroutine, and per-`workplace_id`
  timeouts so one stuck building cannot starve the pool.
- **Dapr absorbs parts of the stack we already run.** Pub/sub over RabbitMQ and state on
  Redis become Dapr components rather than direct clients, which partly obscures the
  three-bus split documented in ADR 0002 and `CLAUDE.md`. That is a real trade, not a
  detail — decide it consciously.
- **A sidecar per pod**, roughly 50 MiB and one extra network hop.
- **Connect through Dapr is settled** — see the spike below. It composes exactly, with no
  code change on either side.
- **`WorkplaceDemolished` finally gets a producer**, and `sim.proto` gains the intent that
  is currently missing. The TTL must be the gateway's decision, never the core's — rule 3
  forbids wall-clock time inside `crates/sim`, so the core receives an intent and applies
  it deterministically at a named tick.
- **The gateway's economy loop becomes idempotent or leader-elected.** `WorkRequest` already
  carries `tick`, so rejecting a repeated `(pip_id, tick)` is the cheap fix and lets
  `replicaCount` exceed 1 honestly.
- **The promise in `workplace.proto:10` becomes true.** "An hour of work in any language" is
  fiction while every instance drags a chart and an entry in the gateway's environment.

## Spike results

Run against `daprd` 1.15.4 in Docker, with the farm and a throwaway multi-instance host on
the machine. Four things checked, three settled, one gap named.

**Connect passes through Dapr untouched.** The conformance suite went 6/6 against the real
farm through the sidecar, with no change to the farm and no change to the test:

```
WORKPLACE_ADDR=localhost:8090                          → 6/6   (baseline)
WORKPLACE_ADDR=localhost:3500/v1.0/invoke/farm/method  → 6/6   (through daprd)
```

It composes rather than merely working. Connect appends `/<package>.<Service>/<Method>` to
its base URL, and Dapr's HTTP invoke path is `/v1.0/invoke/<app-id>/method/<path>` — so the
sidecar address *is* a valid Connect base URL. No gRPC proxying and no `dapr-app-id` header
were needed. This was the blocking risk and it is gone.

**The actor contract and the Connect handler coexist on one port.** They occupy disjoint
paths — `/dapr/config` and `/actors/…` against `/pips.workplace.v1.…` — so one process
serves the external contract and hosts actors off the same mux. `workplace/v1` therefore
stays the public interface and Dapr stays an implementation detail, which is the outcome
rule 1 requires.

**Two instances in one process keep independent state.** Hiring into `farm/1` and `farm/3`
through the actor API, then reading both back through `Describe`, returned 1 and 2 workers
against a shared capacity of 24. Placement registered `[farm]` and served the routing.

**State durability: confirmed, and it costs an indirection.** Covered in step 2a below.

### What the spike changed about the plan

`Describe` with no `workplace_id` has no answer once a service hosts many buildings, and the
conformance suite depends on it — `conformance_test.go:52` calls `Describe{}` to learn who it
is talking to. So `List` is not an addition for convenience; it is required, and the suite
needs a companion mode that enumerates instances and then drives each one. This is the one
place where the migration touches a contract rather than an implementation.

(A second failure in that run — `TestAShiftCanBeStartedWorkedAndEnded` on missing need deltas
— was the spike stub returning nothing, not a finding.)

Also worth recording: in Dapr 1.15 actor **reminders** need the separate scheduler service,
not just placement. Timers work without it. If building lifecycle ends up leaning on
reminders, that is a third component to run.

## Spike step 2a: durability, and the constraint it exposed

State written through the actor state store survives losing everything. With three pips on
farm 1 and two on farm 3, killing the host process and destroying the sidecar container,
then starting a fresh process and a fresh container, returned 3 and 2. The state lives in
Redis under `<app-id>||<actor-type>||<id>||<key>` — `farmhost||farm||1||shifts` — as a hash.

So `store.go` and its two Lua scripts can go. But the shape they are replaced by is not the
one this ADR assumed, and that is the finding.

### Actor state can only be touched from inside an actor invocation

Calling the state API from the Connect handler fails:

```
POST /v1.0/actors/farm/1/state
→ 400 {"errorCode":"ERR_ACTOR_INSTANCE_MISSING","message":"actor instance is missing"}
```

Dapr will not let code write an actor's state unless that code is executing inside an
invocation the sidecar routed to it — activation is what makes the host authoritative for
that id. Business logic therefore cannot live in the Connect handler. The shape is:

```
gateway → Connect handler → sidecar /v1.0/actors/farm/<id>/method/<m> → actor handler
                                                                        ↕ state store
```

The Connect handler becomes a thin adapter that translates an RPC into an actor invocation
and waits. `workplace/v1` stays exactly as it is — which is the point — but the service
behind it grows a second internal boundary, and every call leaves the process and comes
back. Measured against a `/healthz` baseline through the same client, the round trip adds
**~1.6 ms per call**. At the economy's 1 Hz cycle that is irrelevant; it would not be if
anything ever wanted to call a workplace per tick.

This is a real cost and it is the price of not writing the Lua. Worth restating what is
bought: turn-based concurrency per building id, so the atomic reap-check-claim that
`store.go` implements by hand stops being a problem that exists.

### Conformance against a multi-instance host

4 of 6 pass. Two fail, and only one is a finding:

- `TestDescribeIdentifiesTheWorkplace` — the contract gap already described. Confirmed
  against a real actor-backed host rather than inferred.
- `TestAShiftCanBeStartedWorkedAndEnded` — the spike's stub pays a flat need delta instead
  of pricing by elapsed ticks. An artifact of the throwaway, not a property of the design.

## Next steps

1. ~~**Spike**~~ — done, see above.
2. ~~**Add `List` to `workplace/v1`**~~ — done. Additive, `buf breaking` green. The
   conformance suite enumerates through it and falls back to `Describe{}` on Unimplemented,
   which is how the tavern stays green without implementing anything.
3. ~~**`farm` hosts many buildings**~~ — done, without Dapr. `farm.Host` owns a map of
   `Service`, routes on `workplace_id`, and rotates offers between buildings. The gateway
   builds a driver per building via `economy.Discover` rather than one per address. The
   chart takes a `workplaces` list.

   Worth recording why this landed before the actor store: `store.go` was *already* keyed
   `pipsim:workplace:<id>:shifts`, so multi-instance needed a map and no new infrastructure
   at all. Splitting it out means the Dapr migration changes one thing rather than two.

   Known limit: **discovery happens once, at gateway startup**. A building added to a
   running service is invisible until the gateway restarts. Acceptable while ids are
   configuration; not acceptable once `BuildWorkplace` exists.

4. ~~**Swap the store for Dapr actors**~~ — done, as a fourth `Store` rather than a
   replacement. `farm.NewDaprStore` keeps a building's shifts in the actor state store;
   `farm.ActorHost` is the Connect adapter, and `Host.Handler()` serves what the sidecar
   calls back into. Selected by the presence of `DAPR_HTTP_PORT`, which the sidecar injects
   — so there is no separate flag to fall out of step with reality.

   Verified against `daprd` 1.15.4: conformance 6/6 through the actors with two buildings,
   and occupancy of 3 and 2 survived killing both the farm process and the sidecar
   container. State lands in Redis under `farm||farm||<id>||shifts`.

   Two things came out differently from the plan:

   - **The Lua stayed.** The rules moved into `shiftSet`, shared by the memory and Dapr
     stores, and the Redis store still restates them in Lua because that is where *its*
     serialisation has to happen. Deleting it would mean deleting the ability to run without
     a Dapr control plane, which `make test` and `make dev` depend on. Three backings, one
     set of rules, three sources of atomicity — mutex, Lua, actor runtime.
   - **`/healthz` had to change too.** Counting workers through the inner host reads the
     state store outside an invocation, so a perfectly healthy service reported a failure.
     There is no shortcut round the sidecar, not even for a health endpoint.

   Off by default in the chart (`dapr.enabled`), because it needs the control plane
   installed and the chart must stand up without one.
5. ~~**`tavern` in Elixir** against the hand-written actor contract~~ — done. The claim
   survives.

   `Tavern.Dapr` is the whole integration: about 170 lines and **no new dependency**,
   because `:httpc` ships with OTP. Three HTTP calls, one JSON body declaring the entity,
   and two routes on a `Plug` that already existed. Conformance 6/6 through the actors with
   two taverns, and 3 and 2 occupants survived killing both the release and the sidecar
   container.

   So the answer to "does a building type stay an hour of work in any language once Dapr is
   in the picture" is yes — and notably, the language without an SDK needed *less* glue than
   the one with one, because Go's SDK was never used either and the hand-written path is
   simply what both do.

   Three things cost real time and are worth carrying forward:

   - **The sidecar calls the app with `PUT`.** A caller invokes an actor with `POST`; the
     callback into the app is `PUT`. The Go host never noticed because its handler ignored
     the verb; Elixir pattern-matched on `POST` and returned 404, which Dapr reports back as
     `ERR_ACTOR_INVOKE_METHOD` "actor method not found" — indistinguishable, from the
     caller's side, from an entity that was never registered.
   - **Persisted lease time must be wall clock.** `System.monotonic_time/1` is right for
     state that dies with the process and wrong the moment it is written down: a lease
     stored before a restart is compared against a reading taken after one. Caught before
     it ran, but only because the durability test was already the acceptance criterion.
   - **Dapr components are namespace-wide.** Two workplace charts each shipping an unscoped
     `statestore` collide. Both are now named apart and carry `scopes`.

   And one thing worth stating plainly, because it is the honest cost of this ADR: a
   `GenServer` per building under a `DynamicSupervisor` already *is* the actor model, and it
   is better than Dapr at everything except surviving the node — 2 KiB per building,
   microsecond activation, no network hop, real crash isolation. What Dapr buys the tavern
   is placement, and it charges a sidecar round trip for it. For the farm that trade is
   clear, because Go had nothing equivalent. For the tavern it is a trade, not an upgrade.
