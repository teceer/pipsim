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

- `Broadcast.WorldChannel` — one Channel topic per world region, so a client
  subscribes to what it can see rather than to everything
- `Broadcast.Presence` — who is watching, and where
- `Broadcast.GatewayClient` — consumes the `StreamWorld` server stream from
  world-gateway and pushes deltas into PubSub

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
