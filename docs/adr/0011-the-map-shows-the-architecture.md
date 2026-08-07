# 11. The map shows the architecture

Date: 2026-08-07

## Status

Accepted.

## Context

The map draws buildings. Every building on it today is a workplace: the gateway
calls `Describe` on a workplace service, forwards what it says to sim-core as a
`RegisterWorkplaceIntent`, and the client renders whatever comes back in
`WorldDelta.workplaces`. A building is therefore, by construction, a thing that
employs pips.

That made the map a picture of the *simulation*. But this project exists to
learn distributed systems, and the interesting half of it is invisible: the
bank holds the ledger, broadcast fans out deltas, the pathfinder answers route
queries — none of them appear anywhere. A reader of the map sees a farm and a
tavern and would not guess there are seven services behind them.

The bank made this concrete. It is a microservice with its own schema, its own
Kafka topic and its own failure modes, and pips already interact with it on
every wage payment — through the gateway, invisibly. There is no reason for a
player to know it exists.

So: buildings should also stand for services that are not workplaces. The map
becomes a picture of the architecture as well as of the world.

## The problem with the obvious route

The cheap way is to let the bank implement `pips.workplace.v1` — answer
`Describe` with `max_workers: 0` and add it to `WORKPLACE_ADDRS`. It would be on
the map within the hour.

It is also the exact move `services/workplaces/CLAUDE.md` forbids:

> **If you want to extend `workplace.proto` for one specific building, stop.**

And the contract is not merely stretched, it is broken. The gateway drives every
address in `WORKPLACE_ADDRS`: it publishes offers to `offer.bank`, calls
`StartShift` when a pip accepts, `Work` on every cycle, `EndShift` on a lease
expiry. A bank answering `Unimplemented` to four of the five shift RPCs is not a
workplace with zero seats; it is a workplace that lies. Every consumer of the
contract — the conformance suite included — would have to learn the exception.

The shape of the problem is that "employs pips" and "stands in the world" were
one idea, and they are two.

## Decision

**Split them.** `Structure` is a thing that occupies a position in the world.
`Workplace` remains what it always was, and is now the special case: a structure
that also employs.

A new intent, `RegisterStructureIntent`, puts a service on the map. It carries
what the world needs to draw it and nothing about work:

```proto
message Structure {
  uint64 id = 1;
  // The service this stands for: "bank", "broadcast", "world-gateway".
  string kind = 2;
  Vec2 position = 3;
  // Free text under the label — "double-entry ledger", "Phoenix channels".
  string role = 4;
  // Whether the service answered its last health check. The map is a live
  // diagram, so a service that is down should look like it.
  bool healthy = 5;
}
```

The gateway owns registration, because it is already the only service that
knows where every other one lives. It reads a list from configuration, polls
each `/healthz`, and registers a structure per entry. Nothing is discovered by
magic and no service has to implement anything new to appear.

`sim-core` holds structures in the world for the same reason it holds
workplaces: position is world state, and the client predicts against the same
core. They are inert — no capacity, no occupancy, nothing to schedule — but they
are *there*, which is what makes the next step possible rather than a rewrite.

## Consequences

The map answers a question it could not answer before: what is actually
running. A dead service is a dark building.

`sim-core` learns about entities that do not affect the simulation, which is a
real cost — the deterministic core now carries state that exists for the
renderer. The alternative was a second overlay in the client, drawn from
`/healthz` alongside the world, and it was rejected for one reason: a pip can
never walk into an overlay. Putting structures in the world keeps open the thing
this is obviously heading towards — a pip walking to the bank to be paid, or
queueing at the pathfinder — without having to move them later.

Determinism is unaffected. A structure is registered by an intent like any
other, applied on a tick boundary, and `healthy` changes the same way capacity
does: the gateway observes, the core records. Replaying the same intent log
reproduces the same structures.

`BuildWorkplace` is untouched. Structures are not built by players; they are
deployed by whoever runs the cluster, which is the honest model of what a
microservice is.
