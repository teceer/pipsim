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

**Elixir** is here, but does not come from buf. The BSR hosts no Elixir plugin,
so it is the local `protoc-gen-elixir` escript — and that escript writes the
full package path itself on top of the path protoc already chose, producing
`pips/sim/v1/pips/sim/v1/sim.pb.ex`. That duplication was originally blamed on
buf; it is the plugin, and plain protoc does it too.

`make gen-elixir` therefore runs protoc into a scratch directory and lifts the
inner tree out. Less clever than fighting the plugin's path logic, and it stops
working the day the plugin stops doubling rather than the day it changes how.

The escript is a prerequisite:

```
mix escript.install hex protobuf
```

`services/workplaces/tavern` compiles this tree directly via `elixirc_paths`,
which is what keeps "one source of truth for contracts" true for the awkward
language and not only for the convenient ones.
