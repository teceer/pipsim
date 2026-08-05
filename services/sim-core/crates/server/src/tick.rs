//! The tick driver.
//!
//! The server is authoritative at a low tick rate (10 Hz by default) and the
//! browser interpolates between ticks using the same `sim` code compiled to
//! WASM. That is why this loop does not try to run at frame rate — raising the
//! tick rate would buy nothing visually and would multiply Kafka traffic.

use std::sync::{Arc, Mutex};
use std::time::Duration;

use rdkafka::producer::FutureProducer;
use sim::{Intent, World};
use tracing::Instrument;

use crate::events;

pub struct Driver {
    pub world: Arc<Mutex<World>>,
    pub pending: Arc<Mutex<Vec<Intent>>>,
    pub producer: FutureProducer,
    pub period: Duration,
}

impl Driver {
    pub async fn run(self) {
        let mut ticker = tokio::time::interval(self.period);
        // If a tick overruns, skip rather than burst-catching-up: bursting would
        // deliver several ticks with no wall-clock gap and make the client
        // stutter.
        ticker.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);

        loop {
            ticker.tick().await;
            self.step_once().await;
        }
    }

    /// One tick, wrapped in its own span.
    ///
    /// A span per tick is why events carry a usable trace id: everything
    /// published here belongs to this span, so an envelope sitting in a topic
    /// can be taken straight to Jaeger.
    async fn step_once(&self) {
        let span = tracing::info_span!(
            "pipsim.sim-core.tick",
            otel.kind = "internal",
            tick = tracing::field::Empty,
            pips = tracing::field::Empty,
            events = tracing::field::Empty,
            state_hash = tracing::field::Empty,
        );

        async {
            // Drain intents accumulated since the previous tick and apply them
            // all at the tick boundary. Order within a tick is arrival order,
            // which the event log records, so replay reproduces it exactly.
            let intents = {
                let mut p = self.pending.lock().unwrap();
                std::mem::take(&mut *p)
            };

            // The lock is released before any await: holding a std Mutex across
            // an await point would be a deadlock waiting to happen.
            let (tick, pips, events, hash) = {
                let mut w = self.world.lock().unwrap();
                let events = w.step(&intents);
                (w.tick, w.len(), events, w.state_hash())
            };

            let span = tracing::Span::current();
            span.record("tick", tick);
            span.record("pips", pips);
            span.record("events", events.len());
            // Logged every tick on purpose: when a replay diverges, this pins
            // down the exact tick where it happened.
            span.record("state_hash", format!("{hash:016x}"));

            if !events.is_empty() {
                let sent = events::publish(&self.producer, tick, &events).await;
                tracing::info!(tick, produced = events.len(), sent, "facts published");
            }
        }
        .instrument(span)
        .await
    }
}
