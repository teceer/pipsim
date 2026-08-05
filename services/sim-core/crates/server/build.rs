//! Rust is the one language that does not generate into `gen/`.
//!
//! `tonic-build` reads `proto/` directly at compile time, which is the
//! idiomatic Rust workflow and does not fit buf's plugin model. The tradeoff is
//! that `gen/` is not uniform across all four languages — worth knowing before
//! you go looking for Rust bindings that are not there.

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let proto_root = "../../../../proto";

    tonic_build::configure()
        .build_server(true)
        .build_client(true)
        .compile_protos(
            &[
                format!("{proto_root}/pips/sim/v1/sim.proto"),
                format!("{proto_root}/pips/events/v1/events.proto"),
            ]
            .iter()
            .map(|s| s.as_str())
            .collect::<Vec<_>>(),
            &[proto_root],
        )?;

    println!("cargo:rerun-if-changed={proto_root}");
    Ok(())
}
