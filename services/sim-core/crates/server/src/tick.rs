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
use tokio::sync::broadcast;
use tracing::Instrument;

use crate::events;
use crate::grpc::full_delta;
use crate::pb::pips::sim::v1 as simpb;

pub struct Driver {
    pub world: Arc<Mutex<World>>,
    pub pending: Arc<Mutex<Vec<Intent>>>,
    pub producer: FutureProducer,
    pub period: Duration,
    pub deltas: broadcast::Sender<simpb::WorldDelta>,
    /// Ticks between arrivals, or 0 to disable.
    ///
    /// Temporary. There is no food in the world yet — nothing produces it and
    /// nobody eats — so every pip starves within roughly a thousand ticks and
    /// the world stays empty forever. A trickle of arrivals keeps it populated
    /// until the farm exists and the economy closes the loop.
    ///
    /// It lives here in the I/O shell rather than in `sim` on purpose: "people
    /// show up from somewhere" is not a rule of the world, it is a stand-in for
    /// one, and the core should not carry stand-ins.
    pub spawn_every: u64,
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
            let mut intents = {
                let mut p = self.pending.lock().unwrap();
                std::mem::take(&mut *p)
            };

            if self.spawn_every > 0 {
                let tick_now = self.world.lock().unwrap().tick;
                if tick_now.is_multiple_of(self.spawn_every) {
                    // Position derived from the tick rather than randomised, so
                    // a restart from the same seed reproduces the same arrivals.
                    intents.push(sim::Intent::Spawn {
                        name: format!("pip-t{tick_now}"),
                        position: sim::Vec2 {
                            x: ((tick_now as i32).wrapping_mul(3137))
                                .rem_euclid(sim::WORLD_W_MILLI),
                            y: ((tick_now as i32).wrapping_mul(1733))
                                .rem_euclid(sim::WORLD_H_MILLI),
                        },
                    });
                }
            }

            // The lock is released before any await: holding a std Mutex across
            // an await point would be a deadlock waiting to happen.
            let (tick, pips, events, hash, delta) = {
                let mut w = self.world.lock().unwrap();
                let events = w.step(&intents);
                (w.tick, w.len(), events, w.state_hash(), full_delta(&w))
            };

            // Fails only when nobody is watching, which is the normal state
            // before a client connects — not worth logging every 100 ms.
            let _ = self.deltas.send(delta);

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
