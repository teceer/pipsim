/**
 * Connection to world-gateway.
 *
 * The browser speaks Connect directly — no gRPC-Web shim, no Envoy — because
 * the gateway serves the same `.proto` over a protocol browsers can actually
 * use. See ADR 0003.
 */

import { type Client, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import type {
	Pip,
	Structure,
	Workplace,
	WorldDelta,
} from "../../gen/ts/pips/sim/v1/sim_pb";
import { WorldService } from "../../gen/ts/pips/world/v1/world_pb";

export type WorldClient = Client<typeof WorldService>;

export type JoinResult = {
	tick: bigint;
	tickHz: number;
	/** Must seed local prediction, or the predicted world is a different world. */
	simSeed: bigint;
	pips: Pip[];
	/**
	 * Buildings as the world sees them — where they stand and who is inside.
	 *
	 * Not `res.workplaces`, which is how each workplace *service* describes
	 * itself: that headcount is the payroll, and includes pips still walking
	 * there. The renderer draws bodies, so it wants this one.
	 */
	buildings: Workplace[];
	/**
	 * The services behind the world, drawn as buildings — see ADR 0011. Empty
	 * when the gateway has no STRUCTURES configured, and empty offline, where
	 * there is no cluster to diagram.
	 */
	structures: Structure[];
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
		buildings: res.buildings,
		structures: res.structures,
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
