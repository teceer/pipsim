# Build context is the REPOSITORY ROOT, not this directory.
#
# build.rs reads ../../../../proto, so the contracts sit outside the service
# tree. That is the price of proto/ being the single source of truth, and it is
# why the Tiltfile and the Makefile both build this with `-f` and a root
# context rather than the obvious `docker build services/sim-core`.

FROM rust:1-bookworm AS builder

# librdkafka is compiled from source by rdkafka-sys via cmake, and wants these
# headers. protoc is deliberately absent: build.rs vendors it, so the image does
# not depend on Debian's protobuf-compiler, which cannot resolve the
# well-known types anyway.
RUN apt-get update -qq && apt-get install -y --no-install-recommends \
      cmake libcurl4-openssl-dev libssl-dev libsasl2-dev zlib1g-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build

# Manifests first, so the dependency layer survives a source-only change.
# librdkafka alone is minutes of compile time; rebuilding it on every edit
# would make the cluster loop unusable.
COPY services/sim-core/Cargo.toml services/sim-core/Cargo.lock ./services/sim-core/
COPY services/sim-core/crates/sim/Cargo.toml ./services/sim-core/crates/sim/
COPY services/sim-core/crates/server/Cargo.toml ./services/sim-core/crates/server/
COPY services/sim-core/crates/wasm/Cargo.toml ./services/sim-core/crates/wasm/

RUN mkdir -p services/sim-core/crates/sim/src \
             services/sim-core/crates/server/src \
             services/sim-core/crates/wasm/src \
 && echo 'pub mod rng { pub struct Rng; }' > services/sim-core/crates/sim/src/lib.rs \
 && echo '' > services/sim-core/crates/wasm/src/lib.rs \
 && echo 'fn main() {}' > services/sim-core/crates/server/src/main.rs \
 && echo 'fn main() {}' > services/sim-core/crates/server/build.rs \
 && cd services/sim-core \
 && cargo build --release -p sim-server 2>/dev/null || true

# Now the real sources and contracts.
COPY proto ./proto
COPY services/sim-core ./services/sim-core

# Drop this crate's fingerprints before rebuilding.
#
# The layer above compiled a stubbed build.rs. Cargo fingerprints build scripts
# the same way it fingerprints anything else, so without this it happily reuses
# the stub's (empty) output and the real build.rs never runs — which surfaces
# much later as a missing OUT_DIR/pipsim.rs. Only sim's and sim-server's
# fingerprints go; every dependency artifact, librdkafka included, is kept.
RUN cd services/sim-core \
 && rm -rf target/release/.fingerprint/sim-server-* target/release/.fingerprint/sim-* \
 && cargo build --release -p sim-server

FROM debian:bookworm-slim AS runtime

RUN apt-get update -qq && apt-get install -y --no-install-recommends \
      ca-certificates libcurl4 libssl3 libsasl2-2 zlib1g \
    && rm -rf /var/lib/apt/lists/*

# Non-root: the pod security context expects it, and nothing here needs root.
RUN useradd --system --uid 10001 --no-create-home pipsim
USER 10001

COPY --from=builder /build/services/sim-core/target/release/sim-server /usr/local/bin/sim-server

ENV SIM_TICK_HZ=10 \
    SIM_SEED=42 \
    RUST_LOG=info

ENTRYPOINT ["/usr/local/bin/sim-server"]
