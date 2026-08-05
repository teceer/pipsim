/**
 * Connection to world-gateway.
 *
 * The browser speaks Connect directly — no gRPC-Web shim, no Envoy — because
 * the gateway serves the same `.proto` over a protocol browsers can actually
 * use. See ADR 0003.
 */

import { createClient, type Client } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import { WorldService } from "../../gen/ts/pips/world/v1/world_pb";
import type { Pip, WorldDelta } from "../../gen/ts/pips/sim/v1/sim_pb";

export type WorldClient = Client<typeof WorldService>;

export type JoinResult = {
  tick: bigint;
  tickHz: number;
  /** Must seed local prediction, or the predicted world is a different world. */
  simSeed: bigint;
  pips: Pip[];
};

export function connect(baseUrl: string): WorldClient {
  return createClient(
    WorldService,
    createConnectTransport({
      baseUrl,
      // Binary rather than JSON: deltas arrive 10 times a second and carry
      // every pip, so the encoding cost is not incidental.
      useBinaryFormat: true,
    }),
  );
}

export async function join(
  client: WorldClient,
  clientId: string,
): Promise<JoinResult> {
  const res = await client.joinWorld({ clientId });
  return {
    tick: res.tick,
    tickHz: res.tickHz || 10,
    simSeed: res.simSeed,
    pips: res.pips,
  };
}

/**
 * Yields authoritative deltas until the stream ends or `signal` aborts.
 *
 * Deliberately an async generator: the caller decides what to do with
 * backpressure. Buffering deltas the renderer cannot keep up with would make
 * the world drift steadily further into the past.
 */
export async function* streamDeltas(
  client: WorldClient,
  clientId: string,
  fromTick: bigint,
  signal: AbortSignal,
): AsyncGenerator<WorldDelta> {
  const stream = client.streamWorld({ clientId, fromTick }, { signal });
  for await (const msg of stream) {
    if (msg.delta) yield msg.delta;
  }
}
