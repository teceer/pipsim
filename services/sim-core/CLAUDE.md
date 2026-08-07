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

What it does do: drives the tick loop, wraps each tick in a span, maps
`sim::DomainEvent` onto `pips.events.v1.EventEnvelope`, and publishes to Kafka
keyed by aggregate id. Each envelope carries the trace id of the tick that
produced it, so an event sitting in a topic can be taken straight to Jaeger.

Two constraints worth knowing before you touch it:

- The OpenTelemetry crates are version-locked to each other. `opentelemetry`,
  `opentelemetry_sdk` and `opentelemetry-otlp` share a minor; `tracing-opentelemetry`
  runs one ahead. Bumping one alone produces trait mismatches that read as if
  the API disappeared.
- The producer compresses with lz4, not zstd, because rdkafka's bundled
  librdkafka is built without libzstd — asking for zstd fails at client
  construction. The topics are configured zstd, so the broker recompresses.

## Determinism is the product

The whole replay and event-sourcing story rests on one property: the same seed
plus the same intent sequence produces byte-identical worlds. The test
`same_seed_and_intents_produce_identical_worlds` guards it. If that test ever
fails, do not weaken it — something else is genuinely broken.

`World::state_hash()` is logged every tick so a divergent replay can be pinned
to the exact tick where the two runs parted.

## Buildings, and who owns "how many fit"

The core keeps a workplace registry — position, capacity, and how many pips are
physically inside. It does **not** decide the capacity. That number is the
workplace service's own `max_workers`, and the gateway copies it in with a
`RegisterWorkplace` intent, which is idempotent and re-sent on a loop so a
restarted core is repopulated without anyone coordinating.

That split is what keeps rule 4 intact. The farm owns *employment* capacity —
how many it takes on — and the core owns *physical* occupancy, enforcing the
same number. One owner, two places it bites.

The consequence worth remembering: **`employers[i]` and `inside[i]` are
different facts.** A hired pip walks to the building, and if it arrives to a
full one it queues at the door — employed, outside, `Activity::Commuting`. The
gateway relies on this: it keeps calling `Work` while a pip commutes, because
the farm's shift lease is shorter than a long walk, but it applies the need
deltas only once the pip is actually inside.

## Structures, which are not buildings

The core also keeps a `Structure` registry: one entry per microservice, so the
map doubles as a diagram of what is deployed (ADR 0011). They have a position, a
role and a `healthy` flag, and nothing else — no capacity, no occupancy, no
balance.

Two rules about them:

- **Nothing in the tick loop may read one.** They are inert scenery. The test
  `a_structure_does_not_touch_the_simulation` runs two worlds, registers a
  structure in one, and asserts the state hashes still match.
- **They are not in `state_hash()`, on purpose.** `healthy` flips because a
  container restarted, and the browser's WASM core registers no structures at
  all. Mixing them in would make a perfectly matching world look divergent and
  fail `make parity` for a reason unrelated to the core.

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
