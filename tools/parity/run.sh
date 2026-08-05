#!/usr/bin/env bash
#
# Verifies that the native and WASM builds of the simulation core produce
# byte-identical worlds.
#
# The whole client-prediction design rests on this: the browser runs the same
# code as the server, so its prediction is exact rather than approximate. If
# these two hashes ever diverge, that stops being true — and the symptom in the
# game would be pips visibly snapping to corrected positions, which is a much
# more confusing thing to debug than a failing check here.
#
# Usual culprits when it does fail: a float in World, an isize/usize that got
# serialized, or an iteration over a HashMap.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
sim_core="$repo_root/services/sim-core"
build_dir="$sim_core/target/parity-nodejs"

echo "==> native"
native="$(cd "$sim_core" && cargo run --quiet --release -p sim --example parity)"

echo "==> wasm"
(cd "$sim_core" && cargo build --quiet -p sim-wasm --target wasm32-unknown-unknown --release)
wasm-bindgen "$sim_core/target/wasm32-unknown-unknown/release/sim_wasm.wasm" \
  --target nodejs --out-dir "$build_dir" --no-typescript
wasm="$(node "$repo_root/tools/parity/parity.mjs" "$build_dir/sim_wasm.js")"

echo
echo "native hash: $native"
echo "wasm   hash: $wasm"

if [[ "$native" == "$wasm" ]]; then
  echo "OK — native and wasm agree"
else
  echo "DIVERGED — client prediction would be wrong" >&2
  exit 1
fi
