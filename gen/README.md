# gen/

Generated from `proto/` by `make gen`. **Committed on purpose.**

The tradeoff: diff noise, in exchange for coding agents reading generated types
without running `buf`, and for contract breakage showing up directly in a PR
diff. CI fails if this directory is stale.

Do not edit anything here by hand.

## Two exceptions

**Rust** has nothing here. `tonic-build` reads `proto/` directly from
`build.rs`, which is idiomatic for Rust but does not fit buf's plugin model.
Rust bindings are generated at compile time into `target/`.

**Elixir** has nothing here *yet*. The BSR hosts no Elixir plugin, so it would
have to be the local `protoc-gen-elixir` escript — and that escript writes the
full package path itself while buf also nests output per source directory,
producing `gen/elixir/pips/sim/v1/pips/sim/v1/sim.pb.ex`. `strategy: all` does
not change it.

No Elixir service consumes a contract yet, so generating dead code behind a
path-rewriting hack was not worth it. When `services/broadcast` first reads a
proto, add the plugin back to `proto/buf.gen.yaml` and sort the paths out then.
