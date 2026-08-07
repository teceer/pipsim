# 8. sim-core owns building position, not the workplace service

Status: accepted

## Context

Today a building's position is configuration owned by the workplace service, not by the
simulation. `services/workplaces/farm/internal/farm/farm.go:95-106` takes `x, y` as
constructor arguments sourced from env vars in `cmd/farm/main.go:95`;
`services/workplaces/tavern/lib/tavern/buildings.ex` parses the same shape out of
`WORKPLACES=id:x:y,...` (Helm, defaulting to `32000,20000` if unset). Neither service
computes or validates a position — it is handed one at startup and repeats it back.

That position travels one way: workplace → gateway → core. `Describe` reports it in
`DescribeResponse.position` (`workplace.proto:65`); `world-gateway`'s
`economy/driver.go:158-196` reads `desc.GetPosition()` verbatim and copies it into
`RegisterWorkplaceIntent.position` (`sim.proto:200`) on the ten-second re-registration loop
described in ADR 0004. `crates/sim/src/lib.rs:341` (`workplace_positions: Vec<Vec2>`) is a
passive store — updated only by that intent, never computed or checked. Nothing in the path
asks whether two buildings overlap, or whether a position is even on the map.

This is the same shape ADR 0004 already fixed once, for a different field. Before that ADR,
`max_workers` was "a number inside a Go process" and nothing enforced it physically. The fix
was rule 4: one number, one owner, and a second, genuinely different thing (physical
occupancy) enforced by sim-core. Position has not had that pass. A farm does not decide
capacity because employment is its domain; a farm should not decide position either, because
*where a building stands* is not employment, farming, or tavern business — it is a fact
about the map, and the map is sim-core's.

`pips/world/v1/world.proto:63` already has `BuildWorkplaceRequest.position` — a
client-supplied position for building placement — but nothing in the gateway wires it to
anything yet. ADR 0005 names `BuildWorkplace` as a known gap ("acceptable while ids are
configuration; not acceptable once `BuildWorkplace` exists"). The contract already assumes
position comes from outside the workplace service; the implementation has not caught up.

## Decision

**Position becomes a fact sim-core assigns and enforces, never one a workplace service
declares.**

- `workplace/v1.DescribeResponse.position` is deprecated. `buf breaking` forbids removing or
  renumbering the field (rule 1's corollary: contracts change by adding), so it is marked
  `deprecated = true` and reserved for deletion at the next major version. Farm and tavern
  stop populating it; the gateway stops reading it. A workplace's `Describe` answers what it
  *is* (kind, capacity, wage, offers) — not where it stands.
- `world-gateway`'s `economy/driver.go` `Register` stops sourcing position from
  `desc.GetPosition()`. Until `BuildWorkplace` exists, statically-configured buildings get
  their position from the gateway's own config (a `workplace_id → Vec2` map in its Helm
  values), because the gateway is the piece assembling the world out of services — not
  domain logic, and not the workplace's job either way.
  `WORKPLACE_X`/`WORKPLACE_Y`/the `x:y` pairs in `WORKPLACES` are deleted from the farm and
  tavern charts; only `workplace_id` and `kind`-specific config remain there.
- Once `BuildWorkplace` is implemented, a client-requested position
  (`BuildWorkplaceRequest.position`) flows to the gateway, which submits it to sim-core as
  the position half of `RegisterWorkplaceIntent`. sim-core decides whether it is valid — on
  the map, and not within `WORKPLACE_MIN_SPACING_MILLI` of another building — the same way
  it already decides whether a pip fits through a door in ADR 0004: deterministically, inside
  `apply_intents`, using state it already owns (`workplace_positions`). An invalid intent has
  no effect on the world — not a distinguishable `SubmitIntentResponse` outcome, because
  intents are queued and applied at the *next* tick (`crates/server/src/grpc.rs`), so the RPC
  response has already gone out by the time the check runs. It is observable a different way
  (see Consequences). A gateway-side pre-check was rejected for the same reason a Go placement
  algorithm was: it would need to know the same spatial state sim-core already owns, and now
  does.
- `pips.sim.v1.Workplace.position` (`sim.proto:83`), already broadcast in `WorldDelta` and
  `SnapshotResponse`, remains the single authoritative value a client ever reads. Nothing
  changes there — this ADR removes a second, unvalidated source of the same fact, it does
  not add a new public one.

## Consequences

- **Workplace services get simpler, not more capable.** Farm and tavern lose a constructor
  argument and an env var each; neither gains anything, because geometry was never their
  domain. This is a pure subtraction, unlike ADR 0004's `commute`, which gave the core new
  behaviour.
- **The gateway temporarily owns static placement config.** That is a stopgap, not a design
  goal — the same category of thing ADR 0005 calls "acceptable while ids are configuration."
  It moves again, to sim-core validating client-supplied positions, once `BuildWorkplace` is
  real. Landing both at once is too many variables, the same reasoning ADR 0005 used to defer
  shifts-as-workflow.
- **Collision and bounds are enforced, at building-footprint granularity.** `apply_intents`
  rejects a `RegisterWorkplaceIntent` whose position is off the map (outside
  `0..=WORLD_W_MILLI`/`0..=WORLD_H_MILLI`) or within `WORKPLACE_MIN_SPACING_MILLI` (one tile)
  of another building's registered position — insert and update alike, so an existing
  building cannot be walked on top of another one either. `WORKPLACE_MIN_SPACING_MILLI` is a
  placeholder for a real per-kind footprint, which sim-core has no notion of yet; it is
  enough to catch the failure that motivated this ADR — two buildings registered on the same
  spot — without inventing building sizes the rest of the system does not have.
- **Rejection is observable, but not through Kafka.** `crates/sim` gained a
  `WorkplaceRegistrationRejected` `DomainEvent` — still pure data, still no I/O — but
  `crates/server`'s `events::route` returns `None` for it rather than an envelope. Rule 2
  reserves Kafka for facts many independent consumers might care about; a rejected placement
  concerns exactly one audience, whoever runs the cluster, and the gateway's registration loop
  means it would otherwise fail silently forever rather than resolving itself the way a
  `Transfer` failure does. `tick.rs` turns it into a `tracing::warn!` and a
  `pipsim.sim.workplace_registration_rejected` counter instead — the same OTel pipeline
  ADR 0007 already wired up, not a new one.
- **`Describe`'s wire format keeps a dead field until the next major version.** Cheaper than
  a breaking change; conformance tests should assert it is no longer read rather than that it
  is absent.
- **No change for pips or clients.** `Workplace.position` in `WorldDelta` is unaffected;
  this is entirely a rearrangement of who is allowed to *set* a value that was already
  broadcast from one place.

## Alternatives considered

**Keep `workplace/v1.position`, add collision validation in sim-core on top of it.**
Rejected: it fixes enforcement but not ownership. Two values would exist for the same fact —
the workplace's declared position and the core's validated one — and rule 4 is about not
having a second source of truth, not about which one wins an argument.

**Let the gateway compute placement (e.g. a packing/grid algorithm) instead of sim-core.**
Rejected on the same grounds ADR 0004 rejected two-language capacity enforcement: placement
validation needs the same spatial state that already lives in sim-core for pip movement and
occupancy. A second spatial model in Go would drift from what the core actually enforces, and
the gateway is supposed to be routing, not domain logic, per the service map in `CLAUDE.md`.
