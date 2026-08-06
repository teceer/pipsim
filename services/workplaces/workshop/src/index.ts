/**
 * Workshop — a workplace that turns grain into tools.
 *
 * Implements pips.workplace.v1.WorkplaceService and nothing else. It has no
 * idea the farm or the tavern exist; it only knows it consumes GRAIN and
 * produces TOOL.
 */

const MAX_WORKERS = 3;

/** Tools produced per working tick, and what it costs in grain. */
const TOOLS_PER_TICK = 1;
const GRAIN_PER_TOOL = 2;

/**
 * Who is on shift right now. Not persisted on purpose — pips belong to
 * sim-core, so after a restart shifts are re-derived from it rather than
 * trusted from local state.
 */
const onShift = new Set<bigint>();

const server = Bun.serve({
  port: Number(process.env.PORT ?? 8091),
  fetch(req) {
    const url = new URL(req.url);

    if (url.pathname === "/healthz") {
      return Response.json({ status: "ok", workers: onShift.size });
    }

    // TODO: mount the generated Connect handler for WorkplaceService.
    //   Describe   -> kind "workshop", produces TOOL, consumes GRAIN,
    //                 wage per ADR 0006 (no sells: the workshop doesn't
    //                 trade with pips, only with the grain/tool economy)
    //   CanEmploy  -> onShift.size < MAX_WORKERS
    //   Work       -> TOOLS_PER_TICK, need_deltas { FOOD: -2, REST: -1 },
    //                 wage proportional to elapsed ticks
    //   EndShift   -> shift_should_end when grain runs out
    //   Buy        -> always declines; nothing here is for sale to a pip

    return new Response("not found", { status: 404 });
  },
});

console.log(
  JSON.stringify({
    level: "info",
    msg: "workshop listening",
    port: server.port,
    max_workers: MAX_WORKERS,
    grain_per_tool: GRAIN_PER_TOOL,
  }),
);
