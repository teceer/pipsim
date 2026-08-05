# bff

TypeScript on Bun. The client-facing API and the owner of all scheduled work.

## Why a third bus

This is the question worth answering before touching anything here:

| Bus | Semantics | Example |
|---|---|---|
| Kafka | immutable fact log, retained, replayable, many consumers | `PipStartedWork` |
| RabbitMQ | task distribution, competing consumers, per-message ack | "five pips want work at this workshop" |
| BullMQ | **delayed and repeating** jobs | "this building finishes in 30 seconds" |

Delayed execution is what Kafka cannot do without contortions and RabbitMQ only
does awkwardly through dead-letter TTL tricks. Construction timers, crop growth
and shift scheduling all need it. That is BullMQ's whole justification — if you
find yourself adding a queue here that is really a fact log, it belongs in Kafka.

## Boundaries

- **Must not talk to Kafka.** Facts flow through `world-gateway`.
- **Must not decide anything about world state.** When a timer fires, this
  service tells the gateway that it fired; the gateway turns that into an intent
  and sim-core decides what it means.
- Shares generated types with `web/` via `gen/ts` — that is the reason the BFF
  is TypeScript rather than another Go service.

## Idioms used here

- ESM only, `type: "module"`
- native `fetch` and `Bun.serve`, no Express
- JSON logs to stdout matching the shared schema
- Biome for lint and format, not ESLint + Prettier

## Working on it

```bash
make test   # bun test, needs only Redis for queue tests
make lint   # biome + tsc --noEmit
make run    # hot reload
```
