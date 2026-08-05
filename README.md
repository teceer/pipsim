# pipsim

An open-source simulation of *pips* — little people who walk to work, get hungry,
produce things and occasionally starve because you forgot to build a farm.

The game is the excuse. The point is a microservice architecture wired end to
end: gRPC between services, Kafka as the fact log, Kubernetes underneath,
Terraform describing it, and a deterministic core that can replay any world from
its event log.

## What is interesting here

- **The browser predicts with the server's own code.** The simulation core is
  Rust compiled to both a native binary and WASM, so client-side prediction runs
  the identical logic rather than a reimplementation of it.
- **Deterministic replay.** Same seed plus same intents rebuilds the same world
  byte for byte. You can rewind to tick 4,700 and watch exactly why everyone
  starved. Guarded by a test, not by hope.
- **The prediction claim is checked, not asserted.** `make parity` runs the same
  scenario through the native and the WASM build and compares state hashes. If
  they ever diverge, client prediction has started lying — CI fails on it.
- **A workplace is a microservice.** Farm, workshop and tavern each implement one
  shared `.proto` contract, in three different languages. Adding a building type
  touches neither the core nor the gateway.
- **Three brokers, three semantics.** Kafka for facts, RabbitMQ for task
  distribution, BullMQ for delayed jobs — see
  [ADR 0002](docs/adr/0002-three-message-buses.md) for why that is not redundancy.
- **Deliberately polyglot, uniformly operated.** Six languages inside; one
  identical operational shell outside. See
  [ADR 0003](docs/adr/0003-polyglot-with-a-uniform-shell.md).

## Stack

| Layer | Choice |
|---|---|
| Simulation core | Rust (SoA, fixed-point, zero deps) |
| Pathfinding | Rust + a Zig kernel over FFI |
| Gateway | Go + Connect-RPC |
| Fan-out to browsers | Elixir / Phoenix Channels |
| BFF and scheduled jobs | TypeScript on Bun + BullMQ |
| Workplaces | Go, TypeScript, Elixir |
| Client | TypeScript + PixiJS + WASM |
| Buses | Redpanda, RabbitMQ, Redis |
| Storage | Postgres, schema per service |
| Platform | k3d, Helm, Terraform, Tilt, OpenTelemetry |

Connect-RPC rather than plain gRPC, because browsers cannot speak gRPC natively
and the usual fix is gRPC-Web plus an Envoy proxy. Connect handles both
browser-to-service and service-to-service from the same `.proto`, and falls back
to plain HTTP/JSON so any endpoint is reachable with `curl`.

## Getting started

```bash
nix develop          # or install the toolchains listed in flake.nix

make dev             # everything locally via docker compose, no Kubernetes
make test            # every service's tests, no cluster required
```

The cluster is for integration testing, not for iterating on code:

```bash
make infra-up        # k3d + Terraform, applied in three ordered layers
tilt up              # hot reload
make infra-down
```

See the client running against the WASM core — 300 pips, simulation at 10 Hz
interpolated to display rate:

```bash
make -C services/sim-core wasm     # build the prediction module
cd web && bun install && bun run dev
```

And the two checks worth running first:

```bash
make -C services/sim-core determinism   # same seed -> same world
make parity                             # native and WASM agree byte for byte
```

## Repository layout

```
proto/          contracts — the single source of truth
gen/            generated bindings (committed on purpose; Rust is the exception)
services/       one directory per service, uniform Make targets
web/            browser client
infra/          terraform (00-cluster, 10-platform, 20-data), helm, otel
tools/          replay, load generation
docs/adr/       why things are the way they are
```

`CLAUDE.md` at the root describes the architecture and the non-negotiable rules;
each service has its own with that language's idioms. They are written for coding
agents but are the fastest orientation for humans too.

## Status

The chain runs end to end: `sim-core` and `world-gateway` in the cluster, the
browser rendering a world it is served rather than one it invents. sim-core's
state hash on tick 1 is identical whether the binary runs natively on macOS or
inside the Linux container — determinism holds across platforms, not just
across runs.

What is actually verified in CI:

- the simulation core, with tests including the determinism invariant
- native/WASM parity, by comparing state hashes
- contract lint and backward-compatibility, plus `gen/` being current
- all three Terraform layers validating against real provider schemas
- every Go module vetting and building, which also proves `gen/go` compiles
- the web client typechecking against the generated WASM bindings

Not yet real: the workplaces, broadcast and the BFF are contracts plus
skeletons. Nothing produces food, so pips starve and the world is kept
populated by a temporary trickle of arrivals in sim-core's I/O shell — closing
that loop is what the farm is for. The proto definitions are the part worth
reading first.

## License

MIT
