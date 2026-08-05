//! SimService over gRPC.
//!
//! This is the boundary the rest of the system talks to. Commands arrive here
//! and become intents; deltas leave here as a server stream. Facts go to Kafka
//! instead — see `events.rs` and ADR 0002 for why the two are not the same
//! channel.
//!
//! Nothing here decides anything about the world. It translates between wire
//! types and `sim` types, and that is all it is allowed to do.

use std::pin::Pin;
use std::sync::{Arc, Mutex};

use tokio::sync::broadcast;
use tokio_stream::wrappers::BroadcastStream;
use tokio_stream::{Stream, StreamExt};
use tonic::{Request, Response, Status};

use sim::{Activity, Intent, Vec2, World};

use crate::pb::pips::sim::v1 as pb;
use pb::sim_service_server::SimService;

/// Need keys in the wire format's `map<int32, int32>`, mirroring `pips.sim.v1.Need`.
const NEED_FOOD: i32 = 1;
const NEED_REST: i32 = 2;
const NEED_SOCIAL: i32 = 3;

pub struct SimServiceImpl {
    pub world: Arc<Mutex<World>>,
    pub pending: Arc<Mutex<Vec<Intent>>>,
    pub deltas: broadcast::Sender<pb::WorldDelta>,
}

fn activity_code(a: Activity) -> i32 {
    match a {
        Activity::Idle => pb::PipActivity::Idle as i32,
        Activity::Walking => pb::PipActivity::Walking as i32,
        Activity::Working => pb::PipActivity::Working as i32,
        Activity::Eating => pb::PipActivity::Eating as i32,
        Activity::Sleeping => pb::PipActivity::Sleeping as i32,
    }
}

fn needs_map(n: &sim::Needs) -> std::collections::HashMap<i32, i32> {
    // A HashMap is fine here — this is wire encoding, not simulation state, and
    // protobuf maps are unordered by definition. The ban on HashMap applies to
    // `crates/sim`, where iteration order would change the world.
    [
        (NEED_FOOD, n.food),
        (NEED_REST, n.rest),
        (NEED_SOCIAL, n.social),
    ]
    .into_iter()
    .collect()
}

/// Builds a delta describing every pip.
///
/// A real delta would carry only what changed. At 10 Hz with a few hundred
/// mostly-moving pips the difference is small, and sending everything keeps the
/// client's reconciliation trivially correct while the protocol settles. The
/// message shape already supports true diffing, so narrowing this later is a
/// change here and nowhere else.
pub fn full_delta(world: &World) -> pb::WorldDelta {
    let mut pips = Vec::with_capacity(world.len());
    for i in 0..world.len() {
        pips.push(pb::PipDelta {
            id: world.ids[i],
            position: Some(pb::Vec2 {
                x_milli: world.positions[i].x,
                y_milli: world.positions[i].y,
            }),
            activity: Some(activity_code(world.activities[i])),
            needs: needs_map(&world.needs[i]),
        });
    }
    pb::WorldDelta {
        tick: world.tick,
        pips,
        removed_pip_ids: Vec::new(),
    }
}

type DeltaStream = Pin<Box<dyn Stream<Item = Result<pb::WatchDeltasResponse, Status>> + Send>>;

#[tonic::async_trait]
impl SimService for SimServiceImpl {
    /// Not implemented: the tick loop owns stepping.
    ///
    /// Exposing it would allow a caller to advance the world out of band, which
    /// is the one thing that would break replay — the event log would no longer
    /// describe how the world got where it is.
    async fn step(
        &self,
        _: Request<pb::StepRequest>,
    ) -> Result<Response<pb::StepResponse>, Status> {
        Err(Status::unimplemented(
            "the tick driver owns stepping; stepping out of band would break replay",
        ))
    }

    async fn snapshot(
        &self,
        _: Request<pb::SnapshotRequest>,
    ) -> Result<Response<pb::SnapshotResponse>, Status> {
        let w = self.world.lock().unwrap();

        let pips = (0..w.len())
            .map(|i| pb::Pip {
                id: w.ids[i],
                name: w.names[i].clone(),
                position: Some(pb::Vec2 {
                    x_milli: w.positions[i].x,
                    y_milli: w.positions[i].y,
                }),
                activity: activity_code(w.activities[i]),
                needs: needs_map(&w.needs[i]),
                employer_workplace_id: w.employers[i],
            })
            .collect();

        Ok(Response::new(pb::SnapshotResponse {
            tick: w.tick,
            pips,
            // The client compares this against its own prediction to notice
            // that it has drifted far enough to need a resync.
            state_hash: w.state_hash().to_be_bytes().to_vec(),
        }))
    }

    type WatchDeltasStream = DeltaStream;

    async fn watch_deltas(
        &self,
        _: Request<pb::WatchDeltasRequest>,
    ) -> Result<Response<Self::WatchDeltasStream>, Status> {
        // A lagging subscriber is dropped by the broadcast channel rather than
        // slowing the tick loop. A client that cannot keep up should resync from
        // a snapshot, not apply backpressure to the simulation.
        let stream = BroadcastStream::new(self.deltas.subscribe()).filter_map(|res| match res {
            Ok(delta) => Some(Ok(pb::WatchDeltasResponse { delta: Some(delta) })),
            Err(_) => None,
        });

        Ok(Response::new(Box::pin(stream)))
    }

    async fn submit_intent(
        &self,
        request: Request<pb::SubmitIntentRequest>,
    ) -> Result<Response<pb::SubmitIntentResponse>, Status> {
        let Some(intent) = request.into_inner().intent else {
            return Ok(Response::new(pb::SubmitIntentResponse {
                accepted: false,
                rejection_reason: "empty intent".into(),
                scheduled_tick: 0,
            }));
        };

        let translated = match intent {
            pb::submit_intent_request::Intent::Spawn(s) => Intent::Spawn {
                name: s.name,
                position: s
                    .position
                    .map(|p| Vec2 {
                        x: p.x_milli,
                        y: p.y_milli,
                    })
                    .unwrap_or(Vec2::ZERO),
            },
            pb::submit_intent_request::Intent::Move(m) => Intent::Move {
                pip: m.pip_id,
                destination: m
                    .destination
                    .map(|p| Vec2 {
                        x: p.x_milli,
                        y: p.y_milli,
                    })
                    .unwrap_or(Vec2::ZERO),
            },
            pb::submit_intent_request::Intent::Hire(h) => Intent::Hire {
                pip: h.pip_id,
                workplace: h.workplace_id,
            },
            pb::submit_intent_request::Intent::ApplyNeeds(a) => Intent::ApplyNeeds {
                pip: a.pip_id,
                food: a.need_deltas.get(&NEED_FOOD).copied().unwrap_or(0),
                rest: a.need_deltas.get(&NEED_REST).copied().unwrap_or(0),
                social: a.need_deltas.get(&NEED_SOCIAL).copied().unwrap_or(0),
            },
        };

        // Queued, never applied here. Applying mid-tick would make the outcome
        // depend on network timing, which is exactly what determinism forbids.
        let scheduled_tick = {
            let mut p = self.pending.lock().unwrap();
            p.push(translated);
            self.world.lock().unwrap().tick + 1
        };

        Ok(Response::new(pb::SubmitIntentResponse {
            accepted: true,
            rejection_reason: String::new(),
            scheduled_tick,
        }))
    }
}
