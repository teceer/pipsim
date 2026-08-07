//! Mapping simulation facts onto Kafka.
//!
//! The core produces `sim::DomainEvent` and has no idea Kafka exists. This is
//! where those facts become `pips.events.v1.EventEnvelope` and get a topic, a
//! partition key and a trace id.
//!
//! Two rules from the ADRs are enforced here:
//!
//! - The partition key is always the aggregate id, so one pip's events keep
//!   their relative order. Ordering is only guaranteed within a partition, so a
//!   key chosen per-message rather than per-entity would silently interleave a
//!   single pip's history.
//! - Kafka carries the envelope, never a bare payload. The envelope is what
//!   makes replay and cross-service tracing possible.

use std::time::SystemTime;

use rdkafka::producer::{FutureProducer, FutureRecord};
use rdkafka::util::Timeout;
use sim::DomainEvent;

use crate::telemetry;

use crate::pb;
use pb::pips::events::v1::{
    event_envelope::Payload, EventEnvelope, PipDied, PipEndedWork, PipGotHungry, PipSpawned,
    PipStartedWork, PurchaseMade, WorkplaceBuilt,
};
use pb::pips::sim::v1 as simpb;

/// `PurchaseMade.kind` is the generated `workplace.v1` enum, while
/// `crates/sim` carries its own copy — the sim crate takes no dependencies,
/// so the wire enum is not available to it. Same mirroring as
/// `grpc::resource_kind`, in the other direction.
fn resource_kind_pb(kind: sim::ResourceKind) -> pb::pips::workplace::v1::ResourceKind {
    use pb::pips::workplace::v1::ResourceKind as Pb;
    match kind {
        sim::ResourceKind::Grain => Pb::Grain,
        sim::ResourceKind::Food => Pb::Food,
        sim::ResourceKind::Tool => Pb::Tool,
        sim::ResourceKind::Ale => Pb::Ale,
    }
}

pub const TOPIC_LIFECYCLE: &str = "pipsim.pip.lifecycle.v1";
pub const TOPIC_WORK: &str = "pipsim.pip.work.v1";
/// Compacted, keyed by workplace id: the topic holds the current state of every
/// building rather than the history of how it got there. Declared that way in
/// Terraform layer 20 — a consumer rebuilding a map of the world reads it from
/// the beginning and ends up with one entry per building.
pub const TOPIC_BUILDINGS: &str = "pipsim.world.buildings.v1";
/// Purchases, keyed by pip. Separate from the bank's own
/// `pipsim.economy.money.v1` on purpose: the bank consumes this one to book
/// what the core decided, and a service that both produced and consumed one
/// topic would be reading its own writes back.
pub const TOPIC_PURCHASES: &str = "pipsim.economy.purchases.v1";

/// Envelope plus its routing decision: which topic, and which key.
struct Routed {
    topic: &'static str,
    key: String,
    envelope: EventEnvelope,
}

fn now_timestamp() -> prost_types::Timestamp {
    let d = SystemTime::now()
        .duration_since(SystemTime::UNIX_EPOCH)
        .unwrap_or_default();
    prost_types::Timestamp {
        seconds: d.as_secs() as i64,
        nanos: d.subsec_nanos() as i32,
    }
}

/// `None` means the fact never reaches Kafka. Reserved for events that concern
/// operators rather than "many independent consumers" — rule 2 in
/// `CLAUDE.md`. `WorkplaceRegistrationRejected` is the one case today; the
/// tick loop turns it into a log line and a counter instead
/// (`crates/server/src/tick.rs`).
fn route(tick: u64, event: &DomainEvent) -> Option<Routed> {
    let (topic, key, payload) = match event {
        DomainEvent::PipSpawned {
            pip,
            name,
            position,
        } => (
            TOPIC_LIFECYCLE,
            pip.to_string(),
            Payload::PipSpawned(PipSpawned {
                pip_id: *pip,
                name: name.clone(),
                position: Some(simpb::Vec2 {
                    x_milli: position.x,
                    y_milli: position.y,
                }),
            }),
        ),
        DomainEvent::PipGotHungry { pip, food_level } => (
            TOPIC_LIFECYCLE,
            pip.to_string(),
            Payload::PipGotHungry(PipGotHungry {
                pip_id: *pip,
                food_level: *food_level,
            }),
        ),
        DomainEvent::PipDied { pip, cause } => (
            TOPIC_LIFECYCLE,
            pip.to_string(),
            Payload::PipDied(PipDied {
                pip_id: *pip,
                cause: (*cause).to_string(),
                age_ticks: 0,
            }),
        ),
        DomainEvent::PipStartedWork { pip, workplace } => (
            TOPIC_WORK,
            pip.to_string(),
            Payload::PipStartedWork(PipStartedWork {
                pip_id: *pip,
                workplace_id: *workplace,
                workplace_kind: String::new(),
            }),
        ),
        DomainEvent::PipEndedWork {
            pip,
            workplace,
            reason,
        } => (
            TOPIC_WORK,
            pip.to_string(),
            Payload::PipEndedWork(PipEndedWork {
                pip_id: *pip,
                workplace_id: *workplace,
                reason: (*reason).to_string(),
                ticks_worked: 0,
            }),
        ),
        // Keyed by the workplace, not a pip: the aggregate here is the
        // building, and compaction keeps the newest record per key.
        DomainEvent::WorkplaceBuilt {
            workplace,
            kind,
            position,
            capacity,
        } => (
            TOPIC_BUILDINGS,
            workplace.to_string(),
            Payload::WorkplaceBuilt(WorkplaceBuilt {
                workplace_id: *workplace,
                kind: kind.clone(),
                position: Some(simpb::Vec2 {
                    x_milli: position.x,
                    y_milli: position.y,
                }),
                max_workers: *capacity as i32,
            }),
        ),
        DomainEvent::PurchaseMade {
            pip,
            workplace,
            amount,
            resource,
        } => (
            TOPIC_PURCHASES,
            pip.to_string(),
            Payload::PurchaseMade(PurchaseMade {
                payer: format!("pip:{pip}"),
                payee: format!("workplace:{workplace}"),
                amount: *amount,
                kind: resource_kind_pb(*resource) as i32,
            }),
        ),
        // Handled in `tick.rs` as a log line and a counter, not a Kafka fact.
        // See the doc comment on `route`.
        DomainEvent::WorkplaceRegistrationRejected { .. } => return None,
    };

    Some(Routed {
        topic,
        key,
        envelope: EventEnvelope {
            // UUIDv7 sorts by creation time, so a consumer deduplicating on this
            // gets useful locality for free.
            event_id: uuid::Uuid::now_v7().to_string(),
            // The tick, not the wall clock, is the ordering authority. The
            // timestamp below is for humans reading the console.
            tick,
            occurred_at: Some(now_timestamp()),
            trace_id: telemetry::current_trace_id(),
            producer: telemetry::SERVICE_NAME.to_string(),
            payload: Some(payload),
        },
    })
}

/// Publishes a tick's worth of facts.
///
/// Delivery is fire-and-forget with a zero timeout: this sits inside the tick
/// loop, and blocking the simulation on a broker acknowledgement would let
/// Kafka backpressure dictate the simulation rate. Losing an event is a
/// monitoring problem; stalling the world is a gameplay one.
pub async fn publish(producer: &FutureProducer, tick: u64, events: &[DomainEvent]) -> usize {
    use prost::Message;

    let mut sent = 0;
    for event in events {
        let Some(routed) = route(tick, event) else {
            continue;
        };
        let mut buf = Vec::with_capacity(routed.envelope.encoded_len());
        if routed.envelope.encode(&mut buf).is_err() {
            tracing::error!(topic = routed.topic, "failed to encode envelope");
            continue;
        }

        let record: FutureRecord<String, Vec<u8>> = FutureRecord::to(routed.topic)
            .key(&routed.key)
            .payload(&buf);

        match producer
            .send(record, Timeout::After(std::time::Duration::ZERO))
            .await
        {
            Ok(_) => sent += 1,
            Err((err, _)) => {
                tracing::warn!(topic = routed.topic, error = %err, "publish failed");
            }
        }
    }
    sent
}
