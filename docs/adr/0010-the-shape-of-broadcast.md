# 10. The shape of broadcast: fan-in once, interest, and resync

Status: proposed

## Context

`services/broadcast` is a directory with a `mix.exs` and a `.gitkeep`. It has
been named in the service map since the beginning — Phoenix Channels, presence,
fan-out of world deltas to browsers — and never written. Its decisions live in
`services/broadcast/CLAUDE.md`, which is where idioms belong and where
architecture does not.

Meanwhile the thing it exists to prevent is already in the code. Today a browser
streams world state directly from the gateway, and `gateway.go:101` opens **one
upstream `WatchDeltas` per client**:

```go
func (s *Server) StreamWorld(...) error {
    upstream, err := s.sim.WatchDeltas(ctx, ...)   // one per connected browser
```

So the simulation's fan-out is O(clients) at the process that must not be slowed
down by any of them. A hundred spectators is a hundred gRPC streams into
sim-core, each carrying a full copy of the same bytes. The gateway cannot spread
that load either: `infra/helm/world-gateway/values.yaml:31` pins
`replicaCount: 1`, and ADR 0005 explains why — two replicas would both run
`Fleet.Run` and pay every worker twice.

Three more properties of what exists shape what broadcast can be:

- **Deltas are already state, not events.** `grpc.rs:223` drops a lagging
  subscriber rather than slowing the tick loop, on the explicit reasoning that a
  client which cannot keep up should resync from a snapshot. That is the right
  posture and this ADR extends it rather than revisiting it.
- **There is no delta history.** `WatchDeltasRequest.from_tick` exists in the
  contract and is ignored by the implementation — `watch_deltas` takes
  `_: Request<...>` and always subscribes from now. A client that misses deltas
  cannot ask for them again from anyone.
- **Every delta carries the whole world.** `WorldDelta` holds every pip that
  moved and, by `sim.proto`'s own comment, every building "sent in full". At 48×30
  tiles that is fine; it is also the reason nobody has had to think about
  interest yet.

## Decision

### 1. Fan in once per node, fan out locally

A `broadcast` node opens exactly one `StreamWorld` upstream and serves every
browser connected to it from that one subscription. Fan-out becomes O(nodes) at
sim-core and O(clients) on the BEAM, which is the machine that is good at it.

This is the ordinary shape for large connection fleets: an edge process
subscribes once to an authoritative source and multiplies locally. It is also
the only reason to have this service — if each browser still cost sim-core a
stream, an Elixir hop in the middle would be a pure addition of latency.

### 2. Interest is a grid, and the client subscribes to cells

One Channel topic per world cell — `world:cell:{cx}:{cy}` — with the client
joining the cells its viewport covers plus a ring of neighbours, and leaving
them as it pans. Cells are a fixed size in tiles, chosen so a default viewport
spans a handful, not one and not fifty.

Interest management by spatial partition is the standard answer in games and
has been for decades: it turns "every client receives everything" into "every
client receives what it could see". The grid is deliberately the dumbest version
of it — no visibility graph, no per-entity subscriptions — because the world is
48×30 tiles and the point is to have the seam in place before it matters, not
to be clever now.

The gateway's `StreamWorld` stays as it is, unpartitioned. The core does not
learn about cells; `broadcast` computes the cell of each pip from the position
already in the delta.

### 3. Deltas are droppable state; resync goes through `JoinWorld`

Each Channel push carries its `tick`. A client that sees a gap does not ask
anyone to replay — it calls `JoinWorld` on the gateway for a fresh snapshot and
resumes. That is why the contract's ignored `from_tick` is not a bug to fix
here: with a snapshot endpoint and idempotent state, delta history buys very
little and costs retention everywhere.

The same reasoning as `grpc.rs:223`, one hop further out. A newer delta
supersedes an older one, so under pressure the correct thing to discard is the
*oldest* queued message, never the newest.

### 4. Between nodes: distributed Erlang, not another broker

Phoenix PubSub over `pg` across a cluster formed by `libcluster` — no Redis
adapter, no fourth bus. ADR 0002 justifies three brokers by three distinct
semantics; "tell the other node about a message" is not a fourth semantic, it is
the BEAM's native one.

In practice each node holds its own upstream anyway, so cross-node PubSub
carries almost nothing at first. It matters for presence and for anything a
client sends.

### 5. Binary protobuf on the socket, not JSON

Phoenix's default serializer is JSON; the payload here is already a protobuf
message travelling to a client that already links protobuf for its Connect
calls. A custom serializer passing bytes through avoids a decode/encode round
trip per client per tick — the single hottest path in this service.

### 6. Backpressure is bounded and lossy, by design

Every socket gets a bounded outbound queue. Overflow drops oldest first; a
client that stays behind past a threshold is disconnected and expected to
rejoin with a snapshot. Nothing about a slow browser may propagate back to the
tick loop.

This is the property the whole design rests on, and it is only available
because deltas are state. It would be wrong for the fact log — which is exactly
why that lives in Kafka and this does not.

### 7. Presence is deferred

`Phoenix.Presence` is a CRDT replicated across the cluster; its cost scales with
churn, and large deployments routinely outgrow the default. Nothing in pipsim
needs to know who else is watching. The service ships without it, and gains it
when there is a feature that requires it.

## Why not Kafka to the browser

`broadcast` could consume `pipsim.*` topics directly instead of the gateway's
stream, and ADR 0002 makes Kafka the fact log, so the instinct is reasonable.

Rejected because the render path does not want facts. It wants the current
state of a region at 10 Hz, with the right to skip. Kafka gives durability,
ordering per partition, replay and retention — every one of which is a cost
here and none of which is a benefit: a browser that missed the last two seconds
does not want them, it wants now. The events are also the wrong shape. `PipDied`
and `PurchaseMade` describe what happened; a renderer needs where everything
*is*, which is `WorldDelta` and exists only on the stream.

Worth stating plainly because the two look interchangeable on an architecture
diagram: this is the distinction between a fact log and a state feed, and
pipsim now has both on purpose.

## Why not serve WebSockets from the gateway and skip Elixir

The gateway already terminates client connections and already has the deltas.
Adding a WebSocket handler in Go is a day's work and removes a hop.

Rejected on two counts. `replicaCount: 1` is load-bearing (ADR 0005): the
gateway cannot scale out until its economy loop is idempotent or
leader-elected, so putting the connection fleet there caps the world at what one
process can hold and couples "how many people can watch" to "how many buildings
can be driven". And it deletes the only place in this project where the BEAM is
the obviously right tool — supervision per connection, cheap processes,
built-in distribution. ADR 0003 commits to being polyglot where the language
earns it; this is where Elixir earns it.

## Why not per-client interest instead of a grid

Computing exactly what each client can see, per client, is more precise and
sends fewer bytes.

Rejected as premature and as the wrong shape for a topic-based transport. Cells
are shared: one push serves every subscriber of that cell, which is what makes
fan-out cheap. Per-client filtering makes every message a unicast and moves the
work back into the fan-out node, undoing the reason it exists. Precision can be
added later *within* a cell; it cannot be added back underneath one.

## Consequences

- **`broadcast` needs a Connect client for `StreamWorld`.** The Elixir services
  currently reach `workplace/v1` with `Plug` and no gRPC library at all
  (see the README's claim about the tavern). A server-streaming client is a
  different problem, and #7 has just moved the declared `grpc` dependency to
  1.0 — which is now the first real use of it in the repo, not a lockfile
  entry.
- **The client gains a second connection and a second protocol.** Connect for
  commands and `JoinWorld`, Channels for deltas. That is a real cost in the
  browser and the main argument the "just use the gateway" option has going for
  it. The split is honest, though: one is request/response to an authority, the
  other is a subscription to a feed.
- **`WorldDelta` will need a cell key or a position filter cheap enough to run
  per pip per tick.** Computing it from `position` is trivial today; if the
  world grows, the core is where a cell id would have to be stamped, and that is
  a change to `sim.proto`, not to this service.
- **Nothing in CI covers this service**, because nothing is in it. A test job
  and a `lib/` with real code arrive together or the job is theatre.
- **`mix test` currently fails** on `Broadcast.Application is not available` —
  `mix.exs` declares an application module that does not exist. The first commit
  of real code fixes that as a side effect.
- **Deferred deliberately, each its own decision:** presence, cross-node client
  input, edge deployment near users, and whether the client should interpolate
  across a resync or snap.

## Next steps

1. **`Broadcast.Application` and a supervision tree.** One `:one_for_one` root;
   a crashed connection must never take the fan-out down. This alone makes the
   suite runnable.
2. **`GatewayClient`** — one `StreamWorld` subscription per node, pushing into
   PubSub. Reconnect with backoff; the gateway restarting must not need the
   browsers to.
3. **`WorldChannel`** with grid topics, binary serializer, bounded queues.
4. **The client subscribes to cells** and falls back to `JoinWorld` on a tick
   gap. Until this lands, `StreamWorld` stays the client's path and both work.
5. **A CI job**, once there is something for it to run.
