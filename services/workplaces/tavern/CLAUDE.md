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

## Dapr, without an SDK

There is no Elixir SDK for Dapr, which is why this service is the one that
tests the claim. `Tavern.Dapr` is the whole integration, hand-written, and the
bill came to about 170 lines and **no new dependency** — `:httpc` from OTP
serves it, the same argument `Tavern.Connect` makes about not taking a gRPC
stack to speak what is really just POST.

What was needed:

- three HTTP calls (`GET .../state/<key>`, `POST .../state`, invocation),
- a JSON body at `/dapr/config` naming the entity,
- two routes on the `Plug` that already existed.

Three things that will waste an afternoon if you get them wrong:

- **The sidecar calls the app with `PUT`.** A caller invokes an actor with
  `POST`, but the callback into your app is `PUT` — the two directions do not
  agree, and nothing near the callback documentation says so. Matching only
  `POST` makes Dapr report `ERR_ACTOR_INVOKE_METHOD` "actor method not found",
  which reads like the entity was never registered.
- **Persisted time must be wall clock.** `Tavern.Shifts` uses
  `System.monotonic_time/1`, which is right for state that dies with the
  process. `Tavern.Store.Dapr` must not: a lease written before a restart is
  compared against a reading taken after one, and monotonic time has no meaning
  across that boundary. It leaves every shift either immortal or instantly
  expired.
- **State may only be touched inside an invocation the sidecar routed.**
  Anywhere else Dapr answers `ERR_ACTOR_INSTANCE_MISSING`. So the Connect
  handler becomes `Tavern.Workplace.Actor`, an adapter that forwards, and
  `/healthz` counts through the actors too.

What an SDK would have given and this does not: typed actor proxies, timers and
reminders. Only reminders would be missed, and Dapr 1.15 needs a separate
scheduler service for those anyway.

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

`replicaCount: 1` while `dapr.enabled` is false, and that is load-bearing.
Shift state lives in this process, which is correct at one replica and wrong at
two for exactly the reason the farm was wrong at two: `Work` and `EndShift` are
load-balanced RPCs, so a pip hired by one replica is unknown to the other.

With `dapr.enabled`, shifts move to the actor state store and the limit lifts —
the actor runtime allows one invocation at a time per building wherever the
caller landed, which is what the `GenServer` was providing within one node.

Worth being honest about what that trade is. A process per building under a
supervisor *is* the actor model, and BEAM has had it since before Kubernetes
existed; it is faster, it costs about 2 KiB, and one building crashing does not
disturb the next. What it cannot do is survive the node. Dapr buys placement,
and charges a round trip through the sidecar for it. See ADR 0005.

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
