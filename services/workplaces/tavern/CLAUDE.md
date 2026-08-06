# tavern

Elixir. A workplace that pulls ale, and the only one that restores the social
need rather than draining it.

It exists to test a claim, not to add a building. The workplace contract says a
new building type is an hour of work in any language; until the tavern, that
had one implementation behind it and was therefore unfalsifiable.

## No gRPC library, on purpose

The gateway calls workplaces over the **Connect** protocol, not gRPC. Unary
Connect is an ordinary HTTP POST:

```
POST /pips.workplace.v1.WorkplaceService/Describe
Content-Type: application/proto

<bare protobuf message>
```

No length-prefix framing, no trailers, no HTTP/2 requirement. `Plug` and
`Bandit` answer that in `lib/tavern/connect.ex`; `elixir-grpc` would have
dragged in its own codegen conventions to speak a protocol nobody here asks
for.

Two things that will break interop if you change them:

- **the response content type must be exactly `application/proto`.** connect-go
  compares it as a string, so `put_resp_content_type/2` — which appends
  `; charset=utf-8` — fails every call. Pass `nil` as the charset. A test pins
  this, because the failure message points at the client.
- **`application/json` must keep working.** It is what makes `curl` a debugger
  against this service, and it is half the argument for Connect in ADR 0003.

## Contracts

Generated into `gen/elixir` at the repo root by `make gen-elixir` (plain
protoc, not buf — see `gen/README.md`) and compiled in through `elixirc_paths`
in `mix.exs`. The path is `Path.expand`ed: Mix resolves a relative entry
pointing outside the project against the build directory and then silently
fails to find the sources.

## Idioms used here

- `GenServer` for shift state — the process *is* the lock, so capacity-check
  and claim are atomic for free
- injected clock (`Tavern.Shifts` takes `:clock`) so tests make fifteen seconds
  pass without sleeping
- JSON logs through `Tavern.LogFormatter`, matching the schema Go and Rust emit
- `mix format` and `credo --strict` both gate CI

## What it deliberately does not share with the farm

The lease, the elapsed-tick pricing and the atomic claim are written again here
rather than extracted into a library. A workplace is a service; two services
sharing an implementation is how "no domain rule in two languages" quietly
becomes "one rule, two deployments, one of them stale". The contract is shared.
The code is not.

## Scaling

`replicaCount: 1`, and that is load-bearing. Shift state lives in this
process, which is correct at one replica and wrong at two for exactly the
reason the farm was wrong at two: `Work` and `EndShift` are load-balanced RPCs,
so a pip hired by one replica is unknown to the other. The farm's fix was Redis
behind an atomic claim script; the same move applies here. See ADR 0002.

## Working on it

```bash
make test   # ExUnit, no cluster and no listener — the suite calls the plug
make lint   # mix format --check-formatted + credo --strict
make run    # binds :8090
make build  # MIX_ENV=prod mix release
```

Then check it against the contract rather than against itself:

```bash
make run &
WORKPLACE_ADDR=localhost:8090 go test ./services/workplaces/conformance -v
```
