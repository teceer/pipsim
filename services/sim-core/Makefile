.DEFAULT_GOAL := build

## run: start the tick loop and the gRPC server
run:
	SIM_SEED=$${SIM_SEED:-42} SIM_TICK_HZ=$${SIM_TICK_HZ:-10} cargo run -p sim-server

## test: unit tests. The `sim` crate has no dependencies, so this runs offline.
test:
	cargo nextest run -p sim || cargo test -p sim

## test-all: including the I/O shell (needs the proto toolchain)
test-all:
	cargo test --workspace

## lint: clippy with warnings as errors
lint:
	cargo clippy --workspace --all-targets -- -D warnings
	cargo fmt --check

## build: release binary
build:
	cargo build --release -p sim-server

## gen: nothing to do — tonic-build reads proto/ from build.rs
gen:
	@echo "sim-core generates protobuf at compile time via build.rs; nothing to do"

## wasm: build the browser prediction module consumed by web/
wasm:
	cargo build -p sim-wasm --target wasm32-unknown-unknown --release
	wasm-bindgen target/wasm32-unknown-unknown/release/sim_wasm.wasm \
	  --target web --out-dir ../../web/src/sim-wasm

## determinism: run the replay invariant on its own, loudly
determinism:
	cargo test -p sim same_seed_and_intents_produce_identical_worlds -- --nocapture

## parity: prove the native and WASM builds produce byte-identical worlds
parity:
	../../tools/parity/run.sh

.PHONY: run test test-all lint build gen wasm determinism parity
