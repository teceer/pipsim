# broadcast

Elixir / Phoenix. Fan-out of world deltas to every connected browser.

## Why Elixir is here and not in the simulation core

This service exists because of what the BEAM is genuinely best at: holding tens
of thousands of long-lived connections, per-connection fault isolation, presence
tracking, and broadcasting one message to many subscribers.

It is deliberately **not** the simulation core. Actor-per-entity looks like a
natural fit for "many little people", but it is the wrong shape: simulation is a
data-locality problem, not a concurrency problem, and BEAM's message ordering
across processes is non-deterministic, which would break replay. See
`docs/adr/0001-rust-for-the-simulation-core.md`.

## Boundaries

- **Read-only.** This service never mutates world state. Player actions go to
  `world-gateway`, never through a Channel handler here.
- No domain logic. If a delta needs interpreting, sim-core already did it.

## Shape

- `Broadcast.GatewayClient` — **one** `StreamWorld` subscription per node,
  splitting each delta by cell and publishing into PubSub. The one-per-node
  part is the reason this service exists: before it, every browser cost
  sim-core its own stream of identical bytes
- `Broadcast.WorldChannel` — one topic per world cell, so a client subscribes
  to what it can see rather than to everything
- `Broadcast.Grid` — position → cell, and the topic format. The core knows
  nothing about cells; growing the world is a change here
- `Broadcast.Endpoint` / `Broadcast.UserSocket` — the socket, plus `/healthz`

`Broadcast.Presence` is **not** here, deliberately. ADR 0010 decision 7 defers
it: a CRDT replicated across the cluster costs what its churn costs, and
nothing in pipsim needs to know who else is watching yet.

## Two things that will surprise you

**The payload is `{:binary, bytes}`, not a map.** That shape is what makes
Phoenix fastlane a delta straight into a binary WebSocket frame; a plain map
goes through JSON and costs a decode plus re-encode per client per tick. ADR
0010 called for a custom serializer to get this — the default V2 serializer
already does it, so there is no serializer of ours to maintain. Keep the tuple.

**Buildings and structures ride along in every cell**, only pips are
partitioned. There are a handful of buildings, they change rarely, and a client
panning into an empty cell would otherwise have to be told separately that the
farm it can see still exists.

## Elixir idioms used here

- supervision tree with `:one_for_one`; a crashed connection must never take
  down the fan-out
- pattern matching on message shapes rather than conditionals on struct fields
- `with` for happy-path chains, tagged tuples `{:ok, _} | {:error, _}`
- no `GenServer` where a plain function will do — this is the mistake people
  coming from OO make in Elixir

## Working on it

```bash
make test   # mix test, no cluster needed
make lint   # mix format --check-formatted + credo
make run    # mix phx.server, needs WORLD_GATEWAY_ADDR
```
