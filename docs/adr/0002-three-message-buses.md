# 2. Three message buses, and why that is not redundant

Status: accepted

## Context

The project runs Kafka, RabbitMQ and BullMQ at once. That looks like collecting
technologies for their own sake, and it would be — unless each does something the
others genuinely cannot.

## Decision

Each bus owns one semantic, and the rule is enforced in review.

| Bus | Semantics | Example |
|---|---|---|
| **Kafka** (Redpanda) | immutable fact log; retained, replayable, many independent consumers read the same stream | `PipStartedWork`, `ResourceProduced` |
| **RabbitMQ** | task distribution; competing consumers, per-message ack/nack, topic routing | "five pips want work at this workshop, first to ack gets it" |
| **BullMQ** (Redis) | delayed and repeating jobs | "this building finishes in 30 seconds", "crops grow every 5 minutes" |

Delayed execution is what Kafka cannot do without contortions and RabbitMQ only
does awkwardly via dead-letter TTL tricks. Competing-consumer semantics with
per-message ack is what Kafka's consumer groups do not give you. Replayable
history is what neither of the others retains.

## The failure mode this guards against

The way this goes wrong is Kafka being used as slow RPC — a service publishes a
message and then waits for a reply on another topic. If that appears, the design
has drifted and the fix is a gRPC call, not another topic.

The complementary rule, stated in the root `CLAUDE.md`:

> **Commands over gRPC, facts over Kafka.** If the caller waits for an answer,
> gRPC. If something already happened and may concern many, Kafka.

## Consequences

- The simulation loop itself stays out of Kafka entirely. At 10 Hz with 500 pips
  the core does tens of thousands of in-memory operations per second, while only
  tens of *facts* per second cross the bus. Putting the tick loop on a broker
  would be three orders of magnitude of pointless traffic.
- Kafka payloads live in their own proto package (`pips.events.v1`), separate
  from the RPC contracts, because events outlive APIs: a message sitting in a
  topic with 7-day retention must still parse in code deployed a week later.
  Those messages evolve by adding fields only.
- Partition key is always the aggregate id, so one entity's events keep their
  relative order.
- Three brokers to run locally. Redpanda instead of Kafka keeps that affordable —
  one binary, no ZooKeeper, Kafka wire protocol.
