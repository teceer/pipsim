# 1. Rust for the simulation core

Status: accepted

## Context

The core has to advance several hundred pips at 10 Hz, stay reproducible so the
Kafka event log can be replayed, and ideally run in the browser so the client can
predict movement between authoritative updates.

Elixir was the first instinct — "many little independent people" pattern-matches
onto actor-per-entity, and the BEAM is built for exactly that shape.

## Decision

Rust, with a structure-of-arrays world and zero dependencies in the core crate.

## Why not Elixir

Two reasons, the second decisive.

**Simulation is a data-locality problem, not a concurrency problem.** The
canonical solution is ECS: components in contiguous arrays, one tight loop over
`positions[]` and `needs[]`, everything in L1. Actor-per-entity does the
opposite — 500 processes, each with its own heap scattered through memory,
communicating by copying messages. You pay scheduler overhead and cache misses
for what ECS does in a single loop.

**Determinism.** BEAM guarantees message ordering only between a given
sender/receiver pair; globally it is non-deterministic. Replay requires that the
same event log rebuilds the same world byte for byte. Suppressing BEAM's
concurrency to get there means paying for a runtime you are not using.

## Why not the other candidates

- **C#/.NET** — the strongest challenger, and genuinely what most commercial
  simulations use. `Span<T>`, structs, SIMD, NativeAOT; `grpc-dotnet`,
  `Confluent.Kafka` and `Npgsql` are reference-quality. Lost on WASM: Blazor
  ships a runtime, starts slower and runs meaningfully below native Rust→WASM.
- **Zig** — best control over memory layout, arena-per-tick, excellent freestanding
  WASM. But pre-1.0 with breaking changes every release, and effectively no
  networking ecosystem. See ADR 0003 for the contained role it got instead.
- **C++** — EnTT is the best ECS that exists and Emscripten the most mature WASM.
  Rejected on agent ergonomics: undefined behaviour produces no readable error,
  and the build system costs more time than the simulation.
- **Go** — 3–5× faster than BEAM here and the easiest to work in, but map
  iteration order is *deliberately* randomized, making determinism harder than in
  Rust, and Go→WASM drags the GC and runtime along.

## Consequences

- The client predicts with the *same code* the server runs, compiled to WASM.
  This removes an entire class of client/server divergence bugs rather than
  mitigating it.
- Fixed-point arithmetic everywhere in the core; floats could differ between
  native and WASM targets.
- No `HashMap` in the core — randomized seeds make iteration order vary.
- Slower iteration than Go or C# would have been. Partly offset by `rustc`
  errors being unusually good feedback, for humans and agents alike.

## Note

At 500 pips any of these languages is fast enough; the language only starts to
matter past roughly 50k entities. Rust won on *ecosystem + determinism + WASM
reuse*, not on raw speed. And the core is the most replaceable component in the
whole system — one function, `step(state, intents) -> (state, events)`. Rewriting
it in Zig and C# against the same test suite and the same replay log is a planned
exercise, not a risk.
