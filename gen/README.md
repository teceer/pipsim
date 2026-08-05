# gen/

Generated from `proto/` by `make gen`. **Committed on purpose.**

The tradeoff: diff noise, in exchange for coding agents reading generated types
without running `buf`, and for contract breakage showing up directly in a PR
diff. `make gen-check` fails CI if this directory is stale.

Do not edit anything here by hand.

**Rust is the exception** — it has nothing in this directory. `tonic-build` reads
`proto/` directly from `build.rs`, which is idiomatic for Rust but does not fit
buf's plugin model. If you are looking for Rust bindings, they are generated at
compile time into `target/`.
