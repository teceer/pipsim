//! Rust is the one language that does not generate into `gen/`.
//!
//! `tonic-prost-build` reads `proto/` directly at compile time, which is the
//! idiomatic Rust workflow and does not fit buf's plugin model. The tradeoff is
//! that `gen/` is not uniform across languages — worth knowing before you go
//! looking for Rust bindings that are not there.

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let proto_root = "../../../../proto";

    tonic_prost_build::configure()
        .build_server(true)
        .build_client(false)
        // One generated file containing the full pips::<pkg>::v1 module tree.
        // events.proto references messages from sim.proto and workplace.proto,
        // and prost emits those as `super::` paths — so all three must be
        // compiled together and included under a matching module nesting.
        .include_file("pipsim.rs")
        .compile_protos(
            &[
                format!("{proto_root}/pips/sim/v1/sim.proto"),
                format!("{proto_root}/pips/workplace/v1/workplace.proto"),
                format!("{proto_root}/pips/events/v1/events.proto"),
            ],
            &[proto_root.to_string()],
        )?;

    println!("cargo:rerun-if-changed={proto_root}");
    Ok(())
}
