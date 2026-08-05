# world-gateway

Go. The only service browsers talk to. Aggregates sim-core and the workplaces,
serves them over Connect-RPC.

## Boundaries

**This service holds no domain logic.** It routes, aggregates and translates.
Every rule about pips lives in sim-core; every rule about a building lives in
that building's own service. If you are writing a decision about game state
here, it is in the wrong place.

It is also the only service allowed to publish to Kafka on behalf of sim-core —
the core produces events but has no I/O, so the gateway forwards them.

## Why Connect-RPC and not plain gRPC

Browsers cannot speak gRPC natively; the usual workaround is gRPC-Web plus an
Envoy proxy. Connect speaks both browser-to-service and service-to-service from
the same `.proto`, and falls back to plain HTTP/JSON, which means you can debug
any endpoint with `curl`. That removes an entire proxy from the architecture.

## Go idioms used here

- `log/slog` with a JSON handler — matches the shared log schema
- `context.Context` first parameter, always propagated; never `context.TODO()`
  outside of tests
- errors wrapped with `fmt.Errorf("...: %w", err)`, checked with `errors.Is`
- no global state; dependencies passed explicitly into constructors
- h2c so HTTP/2 works without TLS inside the cluster

## Working on it

```bash
make test   # go test -race, no cluster needed
make lint   # go vet + golangci-lint
make run    # needs sim-core reachable at SIM_CORE_ADDR
```

Generated contracts come from `gen/go` at the repo root — do not run `protoc`
here, run `make gen` from the root.
