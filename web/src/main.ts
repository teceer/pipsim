/**
 * Browser client.
 *
 * The world is served by world-gateway: JoinWorld for the initial state, then
 * StreamWorld for authoritative deltas at the simulation's tick rate. Rendering
 * runs at display refresh rate and interpolates between the last two deltas, so
 * a 10 Hz world still looks smooth.
 *
 * The local WASM simulation is still here, seeded with the number the server
 * reports. It is the same code sim-core runs, compiled from services/sim-core,
 * which is what makes prediction exact rather than approximate — there is no
 * second implementation of the movement rules to drift apart from the server's.
 *
 * With no gateway reachable the client falls back to driving that local world
 * itself, which keeps the renderer developable without a cluster.
 */

import { Application, Container, Graphics, Text } from "pixi.js";
import init, {
	SimHandle,
	pip_stride,
	workplace_stride,
} from "./sim-wasm/sim_wasm.js";
import { type WorldClient, connect, join, streamDeltas } from "./world";

// --- world constants --------------------------------------------------------

const MILLI_PER_TILE = 1000;

// Must match sim::WORLD_W_MILLI / WORLD_H_MILLI, or pips walk off the grid.
const GRID_W = 48;
const GRID_H = 30;

/**
 * 2:1 isometric tile diamond, in pixels. Width is the full diagonal of the
 * diamond, height is half of that — the ratio that keeps the tile art
 * artifact-free at integer scales.
 */
const ISO_TILE_W = 32;
const ISO_TILE_H = 16;

const GATEWAY_URL =
	import.meta.env?.VITE_GATEWAY_URL ?? "http://localhost:8081";

/** Only used in the offline fallback; the served world reports its own rate. */
const FALLBACK_TICK_HZ = 10;
const FALLBACK_PIPS = 300;

/** Mirrors pips.sim.v1.PipActivity, which the WASM build now also matches. */
const ACTIVITY_WALKING = 2;
const ACTIVITY_COMMUTING = 6;

/** Building footprint in tiles, and how tall the block is drawn, in pixels. */
const BUILDING_TILES = 3;
const BUILDING_HEIGHT_PX = 30;

/** Where the offline fallback puts its farm — the same spot the cluster uses. */
const FALLBACK_FARM = {
	id: 1,
	kind: "farm",
	x: 12_000,
	y: 8_000,
	capacity: 24,
};

// --- deterministic client-side randomness -----------------------------------

/**
 * Used only by the offline fallback, and seeded so a reload replays the same
 * run. Anything that affects world state has to be reproducible — that rule
 * does not stop at the WASM boundary.
 */
function mulberry32(seed: number): () => number {
	let a = seed >>> 0;
	return () => {
		a = (a + 0x6d2b79f5) >>> 0;
		let t = Math.imul(a ^ (a >>> 15), 1 | a);
		t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
		return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
	};
}

// --- rendering helpers ------------------------------------------------------

type Pip = {
	x: number;
	y: number;
	activity: number;
	food: number;
	/** Which building the pip is standing in, or 0 for outside. */
	inside: number;
};
type Snapshot = Map<number, Pip>;

type Building = {
	kind: string;
	x: number;
	y: number;
	capacity: number;
	occupants: number;
};
type Buildings = Map<number, Building>;

const NEED_FOOD = 1;

/** From the flat Int32Array the WASM module exposes. */
function snapshotFromWasm(flat: Int32Array, stride: number): Snapshot {
	const out: Snapshot = new Map();
	for (let i = 0; i < flat.length; i += stride) {
		out.set(flat[i], {
			x: flat[i + 1],
			y: flat[i + 2],
			activity: flat[i + 3],
			food: flat[i + 4],
			inside: flat[i + 5],
		});
	}
	return out;
}

function buildingsFromWasm(flat: Int32Array, stride: number): Buildings {
	const out: Buildings = new Map();
	for (let i = 0; i < flat.length; i += stride) {
		out.set(flat[i], {
			// The flat encoding carries no strings; the fallback builds one kind.
			kind: FALLBACK_FARM.kind,
			x: flat[i + 1],
			y: flat[i + 2],
			capacity: flat[i + 3],
			occupants: flat[i + 4],
		});
	}
	return out;
}

/**
 * From a server delta.
 *
 * The gateway currently sends every pip each tick rather than a true diff, so
 * this rebuilds the snapshot wholesale. When real diffing lands, this merges
 * into the previous snapshot instead — the message shape already allows it.
 */
function snapshotFromDelta(delta: {
	pips: {
		id: bigint;
		position?: { xMilli: number; yMilli: number };
		activity?: number;
		needs: Record<number, number>;
		insideWorkplaceId?: bigint;
	}[];
}): Snapshot {
	const out: Snapshot = new Map();
	for (const p of delta.pips) {
		out.set(Number(p.id), {
			x: p.position?.xMilli ?? 0,
			y: p.position?.yMilli ?? 0,
			activity: p.activity ?? 0,
			food: p.needs?.[NEED_FOOD] ?? 1000,
			inside: Number(p.insideWorkplaceId ?? 0n),
		});
	}
	return out;
}

function buildingsFromDelta(
	workplaces: {
		id: bigint;
		kind: string;
		position?: { xMilli: number; yMilli: number };
		capacity: number;
		occupants: number;
	}[],
): Buildings {
	const out: Buildings = new Map();
	for (const w of workplaces) {
		out.set(Number(w.id), {
			kind: w.kind,
			x: w.position?.xMilli ?? 0,
			y: w.position?.yMilli ?? 0,
			capacity: w.capacity,
			occupants: w.occupants,
		});
	}
	return out;
}

/** Blue when fed, amber when peckish, red when close to starving. */
function foodColor(food: number): number {
	const t = Math.max(0, Math.min(1, food / 1000));
	if (t > 0.5) return 0x7dd3fc;
	if (t > 0.3) return 0xfbbf24;
	return 0xf87171;
}

const toTile = (milli: number) => milli / MILLI_PER_TILE;

/**
 * Grid tile -> screen space. Standard 2:1 isometric projection: walking one
 * tile east moves the sprite right-and-down, one tile south moves it
 * left-and-down, so the two axes read as receding along the diamond's edges
 * instead of straight down the screen.
 */
function isoProject(tileX: number, tileY: number): { x: number; y: number } {
	return {
		x: (tileX - tileY) * (ISO_TILE_W / 2),
		y: (tileX + tileY) * (ISO_TILE_H / 2),
	};
}

/** Depth key for painter's-algorithm sorting: farther tiles draw first. */
const isoDepth = (tileX: number, tileY: number) => tileX + tileY;

/**
 * Draws one building as an isometric block, with its front face doubling as an
 * occupancy meter.
 *
 * The meter is not decoration: a building has a hard limit on how many pips fit
 * inside, and pips that arrive at a full one queue at the door. Without
 * something showing fullness, that queue looks like a bug — a clump of pips
 * standing on a roof doing nothing.
 *
 * Redrawn each tick rather than tinted, because `occupants` changes and Pixi's
 * Graphics has no cheap way to resize a filled rectangle in place.
 */
function drawBuilding(g: Graphics, b: Building) {
	const cx = toTile(b.x);
	const cy = toTile(b.y);
	const h = BUILDING_TILES / 2;

	// Corners of the footprint, clockwise from the screen-top of the diamond.
	const corners = [
		isoProject(cx - h, cy - h),
		isoProject(cx + h, cy - h),
		isoProject(cx + h, cy + h),
		isoProject(cx - h, cy + h),
	];
	const roof = corners.map((p) => ({ x: p.x, y: p.y - BUILDING_HEIGHT_PX }));

	g.clear();

	// Two visible walls. The pip-facing corner (index 2, screen-bottom) is the
	// door: it is exactly the position the core walks pips to.
	g.poly([roof[3], roof[2], corners[2], corners[3]]).fill({ color: 0x2b3444 });
	g.poly([roof[2], roof[1], corners[1], corners[2]]).fill({ color: 0x212938 });
	g.poly(roof).fill({ color: 0x3d4a60 });
	g.poly(roof).stroke({ color: 0x5b6b86, width: 1 });

	// Occupancy, as a band rising up the left wall.
	const full = b.capacity > 0 ? Math.min(1, b.occupants / b.capacity) : 0;
	if (full > 0) {
		const lerp = (
			a: { x: number; y: number },
			z: { x: number; y: number },
		) => ({
			x: a.x + (z.x - a.x) * full,
			y: a.y + (z.y - a.y) * full,
		});
		g.poly([
			lerp(corners[3], roof[3]),
			lerp(corners[2], roof[2]),
			corners[2],
			corners[3],
		]).fill({ color: full >= 1 ? 0xfbbf24 : 0x38bdf8, alpha: 0.55 });
	}
}

function drawGrid(parent: Container) {
	const g = new Graphics();
	for (let x = 0; x <= GRID_W; x++) {
		const a = isoProject(x, 0);
		const b = isoProject(x, GRID_H);
		g.moveTo(a.x, a.y).lineTo(b.x, b.y);
	}
	for (let y = 0; y <= GRID_H; y++) {
		const a = isoProject(0, y);
		const b = isoProject(GRID_W, y);
		g.moveTo(a.x, a.y).lineTo(b.x, b.y);
	}
	g.stroke({ color: 0x1e2430, width: 1 });
	parent.addChild(g);
}

// --- main -------------------------------------------------------------------

async function main() {
	await init();

	const app = new Application();
	await app.init({ background: "#11131a", resizeTo: window, antialias: true });
	document.body.appendChild(app.canvas);

	const world = new Container();
	// The diamond's leftmost point (tileX=0, tileY=GRID_H) sits at a negative
	// x offset — shift the whole world right by that amount so nothing is
	// clipped off-canvas, then drop it a bit from the top edge.
	world.position.set((GRID_H * ISO_TILE_W) / 2 + 40, 40);
	app.stage.addChild(world);
	drawGrid(world);

	// One sortable layer for everything that stands on the grid. Isometric depth:
	// whatever is further along x+y is "further back" and must be drawn first, or
	// a pip standing in front of a building would be hidden behind it.
	const entityLayer = new Container();
	entityLayer.sortableChildren = true;
	world.addChild(entityLayer);

	const hudPanel = new Graphics()
		.roundRect(8, 8, 300, 128, 6)
		.fill({ color: 0x0b0d12, alpha: 0.82 });
	app.stage.addChild(hudPanel);

	const hud = new Text({
		text: "",
		style: { fill: "#94a3b8", fontFamily: "monospace", fontSize: 13 },
	});
	hud.position.set(20, 18);
	app.stage.addChild(hud);

	// Shared render state, written either by the stream or by the fallback loop.
	let prev: Snapshot = new Map();
	let curr: Snapshot = new Map();
	let buildings: Buildings = new Map();
	let lastTickAt = performance.now();
	let tickMs = 1000 / FALLBACK_TICK_HZ;
	let source = "connecting…";
	let tick = 0n;

	const applySnapshot = (
		next: Snapshot,
		atTick: bigint,
		nextBuildings: Buildings,
	) => {
		prev = curr;
		curr = next;
		buildings = nextBuildings;
		tick = atTick;
		lastTickAt = performance.now();
	};

	// --- served world ---------------------------------------------------------

	const runServed = async (client: WorldClient) => {
		const joined = await join(client, `web-${Math.floor(Math.random() * 1e6)}`);
		tickMs = 1000 / joined.tickHz;
		source = `${GATEWAY_URL} · seed ${joined.simSeed}`;
		// Draw the map before the first delta arrives, so a slow stream does not
		// show an empty world with buildings missing.
		buildings = buildingsFromDelta(joined.buildings);

		// Seeding the local sim with the server's seed is what makes the WASM copy
		// a prediction of *this* world rather than a different one.
		const predicted = new SimHandle(joined.simSeed);
		void predicted;

		const controller = new AbortController();
		window.addEventListener("beforeunload", () => controller.abort());

		for await (const delta of streamDeltas(
			client,
			"web",
			joined.tick,
			controller.signal,
		)) {
			applySnapshot(
				snapshotFromDelta(delta),
				delta.tick,
				buildingsFromDelta(delta.workplaces),
			);
		}
	};

	// --- offline fallback -----------------------------------------------------

	const runLocal = () => {
		source = "local (no gateway)";
		const sim = new SimHandle(42n);
		const stride = pip_stride();
		const buildingStride = workplace_stride();
		const rand = mulberry32(42);

		for (let i = 0; i < FALLBACK_PIPS; i++) {
			sim.queue_spawn(
				`pip-${i}`,
				Math.floor(rand() * GRID_W) * MILLI_PER_TILE,
				Math.floor(rand() * GRID_H) * MILLI_PER_TILE,
			);
		}

		// The gateway registers buildings from what each workplace service reports.
		// Offline there is no such service, so the fallback stands one in — without
		// it the capacity behaviour is simply invisible here.
		sim.queue_register_workplace(
			FALLBACK_FARM.id,
			FALLBACK_FARM.kind,
			FALLBACK_FARM.x,
			FALLBACK_FARM.y,
			FALLBACK_FARM.capacity,
		);

		let hired = 0;
		setInterval(() => {
			// Hire a trickle, so pips visibly walk over and the door fills up.
			if (hired < FALLBACK_PIPS && sim.tick % 5n === 0n) {
				hired++;
				sim.queue_hire(hired, FALLBACK_FARM.id);
			}
			sim.step();
			applySnapshot(
				snapshotFromWasm(sim.positions(), stride),
				sim.tick,
				buildingsFromWasm(sim.workplaces(), buildingStride),
			);
		}, tickMs);
	};

	// Not awaited. The stream runs for as long as the page is open, so awaiting
	// it here would mean the renderer never starts.
	runServed(connect(GATEWAY_URL))
		.then(() => {
			source = "stream ended";
		})
		.catch((err) => {
			console.warn("gateway unavailable, falling back to a local world", err);
			runLocal();
		});

	// --- rendering: display refresh rate, interpolated between ticks -----------

	const sprites = new Map<number, Graphics>();
	const blocks = new Map<number, { body: Graphics; label: Text }>();

	app.ticker.add(() => {
		// How far we are between the last authoritative tick and the next one.
		const alpha = Math.min(1, (performance.now() - lastTickAt) / tickMs);

		let inside = 0;
		let commuting = 0;

		for (const [id, b] of buildings) {
			let block = blocks.get(id);
			if (!block) {
				const body = new Graphics();
				const label = new Text({
					style: { fill: "#cbd5e1", fontFamily: "monospace", fontSize: 11 },
				});
				label.anchor.set(0.5, 1);
				entityLayer.addChild(body, label);
				block = { body, label };
				blocks.set(id, block);
			}

			drawBuilding(block.body, b);
			const centre = isoProject(toTile(b.x), toTile(b.y));
			block.label.position.set(
				centre.x,
				centre.y - BUILDING_HEIGHT_PX - (BUILDING_TILES * ISO_TILE_H) / 2 - 4,
			);
			block.label.text = `${b.kind} ${b.occupants}/${b.capacity}`;

			// Sorted by the door corner, not the centre: that is the tile a pip
			// stands on, and it is what decides whether it should be drawn in front.
			const depth = isoDepth(
				toTile(b.x) + BUILDING_TILES / 2,
				toTile(b.y) + BUILDING_TILES / 2,
			);
			block.body.zIndex = depth;
			block.label.zIndex = depth;
		}

		for (const [id, now] of curr) {
			let g = sprites.get(id);
			if (!g) {
				g = new Graphics().circle(0, 0, 4).fill(0xffffff);
				entityLayer.addChild(g);
				sprites.set(id, g);
			}

			// A pip that went inside is not on the map any more. Drawing it on the
			// roof would make the capacity limit meaningless to look at.
			if (now.inside !== 0) {
				inside++;
				g.visible = false;
				continue;
			}
			g.visible = true;
			if (now.activity === ACTIVITY_COMMUTING) commuting++;

			const before = prev.get(id) ?? now;
			const tileX = toTile(before.x + (now.x - before.x) * alpha);
			const tileY = toTile(before.y + (now.y - before.y) * alpha);
			const screen = isoProject(tileX, tileY);
			g.position.set(screen.x, screen.y);
			g.zIndex = isoDepth(tileX, tileY);
			g.tint = foodColor(now.food);
			g.alpha =
				now.activity === ACTIVITY_WALKING || now.activity === ACTIVITY_COMMUTING
					? 1
					: 0.6;
		}

		// Pips that died between ticks.
		for (const [id, g] of sprites) {
			if (!curr.has(id)) {
				g.destroy();
				sprites.delete(id);
			}
		}

		hud.text = [
			`tick   ${tick}`,
			`pips   ${curr.size}`,
			`at work ${inside} · walking there ${commuting}`,
			`fps    ${app.ticker.FPS.toFixed(0)}`,
			`rate   ${(1000 / tickMs).toFixed(0)} Hz, interpolated`,
			"",
			source,
		].join("\n");
	});
}

main().catch((err) => {
	console.error(err);
	document.body.innerHTML = `<pre style="color:#f87171;padding:2rem;font-family:monospace">${err}</pre>`;
});
