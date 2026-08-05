//! The tick driver.
//!
//! The server is authoritative at a low tick rate (10 Hz by default) and the
//! browser interpolates between ticks using the same `sim` code compiled to
//! WASM. That is why this loop does not try to run at frame rate — pushing the
//! tick rate up would buy nothing visually and would multiply Kafka traffic.

use std::sync::{Arc, Mutex};
use std::time::Duration;

use sim::{Intent, World};

pub async fn run(
    world: Arc<Mutex<World>>,
    pending: Arc<Mutex<Vec<Intent>>>,
    period: Duration,
) {
    let mut ticker = tokio::time::interval(period);
    // If a tick overruns, skip rather than burst-catching-up: bursting would
    // deliver several ticks with no wall-clock gap and make the client stutter.
    ticker.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);

    loop {
        ticker.tick().await;

        // Drain intents accumulated since the previous tick and apply them all
        // at the tick boundary. Order within a tick is arrival order, which is
        // recorded in the event log, so replay reproduces it exactly.
        let intents = {
            let mut p = pending.lock().unwrap();
            std::mem::take(&mut *p)
        };

        let (tick, events, hash) = {
            let mut w = world.lock().unwrap();
            let events = w.step(&intents);
            (w.tick, events, w.state_hash())
        };

        if !events.is_empty() {
            tracing::debug!(tick, count = events.len(), "domain events produced");
            // TODO: publish to Kafka as pips.events.v1.EventEnvelope, keyed by
            // aggregate id so a single entity's events keep their order.
        }

        // The hash is logged every tick on purpose: when a replay diverges, this
        // is what pins down the exact tick where it happened.
        tracing::trace!(tick, state_hash = hash, "tick complete");
    }
}
