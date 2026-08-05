//! sim-core server: the I/O shell around the deterministic `sim` crate.
//!
//! Responsibilities:
//!   - drive the tick loop at SIM_TICK_HZ
//!   - expose SimService over gRPC
//!   - publish domain events produced by each tick to Kafka
//!
//! It holds no simulation rules of its own. If you are about to add an `if`
//! that decides something about pips here, it belongs in `sim` instead.

use std::sync::{Arc, Mutex};
use std::time::Duration;

use sim::{Intent, World};

mod tick;

fn env_u64(key: &str, default: u64) -> u64 {
    std::env::var(key).ok().and_then(|v| v.parse().ok()).unwrap_or(default)
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    tracing_subscriber::fmt().json().with_current_span(true).init();

    // The seed is configuration, never generated at startup. Two runs with the
    // same seed and the same intents must produce the same world.
    let seed = env_u64("SIM_SEED", 42);
    let tick_hz = env_u64("SIM_TICK_HZ", 10);

    tracing::info!(seed, tick_hz, "starting sim-core");

    let world = Arc::new(Mutex::new(World::new(seed)));
    let pending: Arc<Mutex<Vec<Intent>>> = Arc::new(Mutex::new(Vec::new()));

    // TODO: wire up the Kafka producer and the tonic server. The tick loop is
    // deliberately first — a running simulation with no network is more useful
    // than a server with nothing to serve.
    tick::run(
        world.clone(),
        pending.clone(),
        Duration::from_millis(1000 / tick_hz.max(1)),
    )
    .await;

    Ok(())
}
