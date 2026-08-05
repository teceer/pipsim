//! Protobuf and gRPC types generated at compile time by build.rs.
//!
//! Rust is the one language with nothing in `gen/` — see gen/README.md. The
//! module nesting mirrors the proto packages exactly, because prost emits
//! cross-package references as `super::` paths.

include!(concat!(env!("OUT_DIR"), "/pipsim.rs"));
