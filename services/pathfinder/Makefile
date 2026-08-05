.DEFAULT_GOAL := build

## run: start the pathfinder service
run:
	cargo run --release

## test: Rust tests plus the Zig kernel's own tests
test:
	zig test kernel/flowfield.zig
	cargo test

## lint: clippy + zig fmt
lint:
	cargo clippy --all-targets -- -D warnings
	cargo fmt --check
	zig fmt --check kernel/

## build: build the Zig kernel, then link it into the Rust binary
build:
	zig build-lib kernel/flowfield.zig -O ReleaseFast -fPIC -femit-bin=target/libflowfield.a
	cargo build --release

## bench: the reason two implementations exist — is Zig actually earning its keep?
bench:
	cargo bench

## gen: contracts are generated at compile time via build.rs
gen:
	@echo "pathfinder generates protobuf at compile time via build.rs; nothing to do"

.PHONY: run test lint build bench gen
