# 3. Polyglot inside, monoculture outside

Status: accepted

## Context

Using a wide range of languages is a stated goal of this project, not an
accident. But polyglot has a real cost, and it lands squarely on the thing this
repo also optimizes for: how well coding agents can work in it. Agents are
noticeably stronger in Go and TypeScript than in Elixir, Rust or Zig, and a bug
crossing three languages and two brokers is hard for a human and harder for an
agent.

That cost cannot be eliminated. It can be contained.

## Decision

**Polyglot inside, monoculture outside.**

Every service, whatever the language, exposes an identical operational surface:

- the same Make targets: `run` `test` `lint` `build` `gen`
- a `/healthz` endpoint
- JSON logs with a shared schema
- OpenTelemetry spans named `pipsim.<service>.<operation>`
- `.proto` as the only contract

Nobody — human or agent — needs to know that a service is Rust inside. You ask
it the same questions and get the same answers.

Supporting rules:

1. **Each language must be justified by a capability, in one sentence.** Rust:
   determinism plus WASM reuse. Elixir: connection fan-out on the BEAM. Zig: an
   arena-allocated numeric kernel. Go: service glue. TypeScript: types shared
   with the browser. A language with no such sentence does not get in.
2. **No domain rule is implemented twice.** The moment "I will just quickly write
   the same thing in Go, it is easier" happens, the project is dead. Each rule has
   exactly one owner.
3. **Per-service `CLAUDE.md`**, carrying that language's idioms and the service's
   boundaries. An agent dropped into an Elixir service without it will write
   Elixir that looks like Ruby.
4. **Every service is testable without a cluster.** `make test` must never need
   Kubernetes, or the feedback loop dies and the agent starts guessing.

## Consequences

- Two things stop being optional. **OpenTelemetry from day one**, because with
  six runtimes distributed tracing is the only way to debug anything that crosses
  a boundary. And **Nix**, because six toolchains will otherwise drift until the
  project no longer builds.
- `gen/` is committed. It adds diff noise, bought back by agents reading
  generated types without running `buf`, and by `make gen-check` in CI catching
  staleness.
- Rust is the one exception to `gen/`: `tonic-build` reads `proto/` from
  `build.rs`, which does not fit buf's model. Documented, because it is exactly
  the kind of inconsistency that wastes half an hour.
- Workplaces are the designated place to rotate languages, since they are small,
  isolated, and identically shaped. Blowing one up does not take the game down.
