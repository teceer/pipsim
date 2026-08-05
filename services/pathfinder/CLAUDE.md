# pathfinder

Rust service wrapping a Zig kernel via FFI. Computes flow fields for crowd
movement.

## Why Zig is in this project at all

Zig is here in a deliberately contained role. Its ecosystem for networking —
gRPC, Kafka, Postgres clients — is effectively nonexistent, so it is a bad
choice for anything that touches the wire. But for one hot, self-contained
numeric kernel it is excellent: full control over memory layout, an arena
allocator reset per request, and `comptime` for specializing grid sizes.

The containment is the point. Zig is pre-1.0 and breaks between releases; when
0.16 changes the syntax again, one file breaks rather than the whole project.
See `docs/adr/0003-zig-in-a-contained-role.md`.

## Why flow fields rather than per-pip A*

With hundreds of pips heading to a handful of destinations, running A* per pip
recomputes the same paths over and over. A flow field computes the gradient once
per destination over the whole grid, and every pip just reads its cell. It turns
N searches into one, and it is embarrassingly parallel.

## Boundaries

- **Stateless.** No state between requests. The grid arrives in the request or
  is loaded from a shared snapshot; nothing is cached across calls.
- Owns no domain rules. It answers "which way from here to there", nothing more.

## Layout

- `src/` — Rust: gRPC surface, request validation, FFI bindings
- `kernel/` — Zig: the actual field computation, arena-allocated per call

The FFI boundary is intentionally narrow: flat slices of integers in, flat slice
out. No structs cross it, so a Zig version bump cannot silently change layout.

## Working on it

```bash
make test    # Rust tests + zig test on the kernel
make bench   # compares the Zig kernel against the Rust reference impl
make lint
```

`make bench` exists because the whole point of having two implementations is
knowing whether the Zig one actually earns its keep. Keep the Rust reference
implementation — it is both the correctness oracle and the fallback.
