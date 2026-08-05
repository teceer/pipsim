//! Prints the state hash of a fixed scenario, natively.
//!
//! Its counterpart is `tools/parity/parity.mjs`, which runs the identical
//! scenario through the WASM build. `tools/parity/run.sh` compares the two.
//!
//! This exists because "the client predicts with the server's own code" is the
//! central claim of the architecture, and a claim that is not checked is a
//! claim that quietly stops being true. If the two hashes ever differ, client
//! prediction has started lying and the cause is almost always a float or a
//! platform-dependent integer width that crept into the core.

use sim::{Intent, Vec2, World};

/// Must stay identical to the scenario in tools/parity/parity.mjs.
const SEED: u64 = 42;
const PIPS: i32 = 50;
const TICKS: usize = 500;

fn main() {
    let mut world = World::new(SEED);

    let spawns: Vec<Intent> = (0..PIPS)
        .map(|i| Intent::Spawn {
            name: format!("pip-{i}"),
            position: Vec2 {
                x: i * 137 % 48_000,
                y: i * 91 % 30_000,
            },
        })
        .collect();
    world.step(&spawns);

    for t in 0..TICKS {
        // A deterministic sprinkling of movement orders, so the scenario
        // exercises more than need decay.
        let intents = if t % 7 == 0 {
            let pip = ((t / 7) % PIPS as usize + 1) as u64;
            vec![Intent::Move {
                pip,
                destination: Vec2 {
                    x: (t as i32 * 311) % 48_000,
                    y: (t as i32 * 173) % 30_000,
                },
            }]
        } else {
            Vec::new()
        };
        world.step(&intents);
    }

    println!("{:016x}", world.state_hash());
    eprintln!("native: tick={} pips={}", world.tick, world.len());
}
