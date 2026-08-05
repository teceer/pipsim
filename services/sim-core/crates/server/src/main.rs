//! sim-core server: the I/O shell around the deterministic `sim` crate.
//!
//! Responsibilities:
//!   - drive the tick loop at SIM_TICK_HZ
//!   - publish the facts each tick produces to Kafka
//!   - export spans to the OpenTelemetry collector
//!
//! It holds no simulation rules of its own. If you are about to add an `if`
//! that decides something about pips here, it belongs in `sim` instead.

use std::sync::{Arc, Mutex};
use std::time::Duration;

use anyhow::Result;
use rdkafka::producer::FutureProducer;
use rdkafka::ClientConfig;
use sim::{Intent, Vec2, World};

mod events;
mod telemetry;
mod tick;

fn env_u64(key: &str, default: u64) -> u64 {
    std::env::var(key)
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(default)
}

fn kafka_producer(brokers: &str) -> Result<FutureProducer> {
    let producer = ClientConfig::new()
        .set("bootstrap.servers", brokers)
        // Batch aggressively: a tick produces a handful of small messages and
        // there is no latency requirement on the event log.
        .set("linger.ms", "50")
        // lz4, not zstd: rdkafka's bundled librdkafka is built without libzstd,
        // and asking for it fails at client construction rather than at send.
        // The topics themselves are configured zstd, so the broker recompresses
        // — acceptable at this message rate, and the retained data still gets
        // the better ratio.
        .set("compression.type", "lz4")
        .set("message.timeout.ms", "5000")
        .create()?;
    Ok(producer)
}

#[tokio::main]
async fn main() -> Result<()> {
    let provider = telemetry::init()?;

    // The seed is configuration, never generated at startup. Two runs with the
    // same seed and the same intents must produce the same world.
    let seed = env_u64("SIM_SEED", 42);
    let tick_hz = env_u64("SIM_TICK_HZ", 10);
    let brokers = std::env::var("KAFKA_BROKERS").unwrap_or_else(|_| "localhost:31092".to_string());
    let initial_pips = env_u64("SIM_INITIAL_PIPS", 50);

    tracing::info!(seed, tick_hz, %brokers, initial_pips, "starting sim-core");

    let world = Arc::new(Mutex::new(World::new(seed)));
    let pending: Arc<Mutex<Vec<Intent>>> = Arc::new(Mutex::new(Vec::new()));

    // Seed the population.
    //
    // Temporary: once world-gateway exists these arrive as intents from the
    // player rather than being invented here. Positions are derived from the
    // index rather than randomised, so a restart reproduces the same world.
    {
        let mut p = pending.lock().unwrap();
        for i in 0..initial_pips as i32 {
            p.push(Intent::Spawn {
                name: format!("pip-{i}"),
                position: Vec2 {
                    x: (i * 137) % 48_000,
                    y: (i * 91) % 30_000,
                },
            });
        }
    }

    let driver = tick::Driver {
        world: world.clone(),
        pending: pending.clone(),
        producer: kafka_producer(&brokers)?,
        period: Duration::from_millis(1000 / tick_hz.max(1)),
    };

    // Flush spans on shutdown. Without this the last batch is dropped, and you
    // spend an hour wondering why the interesting trace never reached Jaeger.
    tokio::select! {
        _ = driver.run() => {}
        _ = tokio::signal::ctrl_c() => {
            tracing::info!("shutting down");
        }
    }

    if let Err(err) = provider.shutdown() {
        tracing::warn!(error = %err, "tracer shutdown failed");
    }
    Ok(())
}
