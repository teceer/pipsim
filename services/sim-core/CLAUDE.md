# sim-core

The deterministic heart of the simulation. Rust, three crates:

- `crates/sim` — pure logic, **zero dependencies**, compiles to WASM
- `crates/server` — I/O shell: tick loop, gRPC, Kafka producer, tracing
- `crates/wasm` — browser bindings, so the client predicts with server code

## Boundaries

**`crates/sim` must not:**
- take on dependencies (each one is a new way to break WASM or offline builds)
- use `SystemTime`, `Instant`, or any clock
- use `HashMap` / `HashSet` — their randomized seed makes iteration order vary
  between runs; use `Vec` or `BTreeMap`
- use floats in anything that affects state — native and WASM `f32` can differ
- use an unseeded RNG; the only source of randomness is `rng::Rng` inside `World`

**`crates/server` must not:**
- contain simulation rules. If you are writing an `if` that decides something
  about pips, it belongs in `sim`.

## Determinism is the product

The whole replay and event-sourcing story rests on one property: the same seed
plus the same intent sequence produces byte-identical worlds. The test
`same_seed_and_intents_produce_identical_worlds` guards it. If that test ever
fails, do not weaken it — something else is genuinely broken.

`World::state_hash()` is logged every tick so a divergent replay can be pinned
to the exact tick where the two runs parted.

## Data layout

Structure-of-arrays: `positions[i]`, `needs[i]`, `activities[i]` all describe
the same pip. This is why the design is not actor-per-entity — a tight loop over
contiguous arrays beats message passing between 500 actors by a wide margin, and
it is the reason the core is Rust rather than Elixir.

`index_of` is a linear scan on purpose. At this scale it beats a hash lookup on
cache behaviour, and it keeps ordering deterministic. If profiling ever says
otherwise, replace it with a dense slot map — not a `HashMap`.

## Working on it

```bash
make test           # fast, offline, no cluster
make determinism    # the invariant on its own
make wasm           # rebuild the browser module after changing sim
make lint           # clippy -D warnings
```

Start with `make test`. The core needs neither Kafka nor Kubernetes, so the
iteration loop here is seconds, and it should stay that way.

## Fixed point

Positions are milli-tiles (`1000` == one tile), needs are `0..1000`. Never
introduce a float into `World`. `Vec2::step_towards` uses clamped integer deltas
rather than normalizing a vector, precisely to avoid `sqrt`.
