/**
 * BFF — the client-facing API and the home of all *scheduled* work.
 *
 * This is the third bus in the project and the one people question, so to be
 * explicit about why it exists separately from Kafka and RabbitMQ:
 *
 *   Kafka     — immutable fact log. "This happened." Replayable, retained.
 *   RabbitMQ  — task distribution. "Someone do this now." Competing consumers.
 *   BullMQ    — delayed and repeating jobs. "Do this in 30 seconds."
 *
 * Delayed execution is the thing Kafka cannot do without contortions and
 * RabbitMQ only does awkwardly via dead-letter TTL tricks. Construction timers,
 * crop growth, and shift scheduling all need it, so BullMQ earns its place.
 */

import { Queue, Worker } from "bullmq";

const redis = {
  connection: { url: process.env.REDIS_URL ?? "redis://localhost:6379" },
};

/** Buildings under construction. Enqueued with a delay, never polled. */
export const constructionQueue = new Queue("construction", redis);

/** Crops and other resources that tick on their own schedule. */
export const growthQueue = new Queue("growth", redis);

new Worker(
  "construction",
  async (job) => {
    const { workplaceId, kind } = job.data as {
      workplaceId: string;
      kind: string;
    };

    // The BFF does not decide anything about the world — it tells the gateway
    // that a timer elapsed, and the gateway turns that into an intent.
    await fetch(`${process.env.WORLD_GATEWAY_URL}/internal/construction-done`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ workplaceId, kind }),
    });
  },
  redis,
);

const server = Bun.serve({
  port: Number(process.env.PORT ?? 3000),
  async fetch(req) {
    const url = new URL(req.url);

    // Part of the operational contract shared by every service in this repo.
    if (url.pathname === "/healthz") {
      return Response.json({ status: "ok" });
    }

    return new Response("not found", { status: 404 });
  },
});

console.log(
  JSON.stringify({ level: "info", msg: "bff listening", port: server.port }),
);
