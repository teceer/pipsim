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
import init, { SimHandle, pip_stride } from "./sim-wasm/sim_wasm.js";
import { connect, join, streamDeltas, type WorldClient } from "./world";

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

/** Mirrors sim::Activity. */
const ACTIVITY_WALKING = 1;

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

type Pip = { x: number; y: number; activity: number; food: number };
type Snapshot = Map<number, Pip>;

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
  }[];
}): Snapshot {
  const out: Snapshot = new Map();
  for (const p of delta.pips) {
    out.set(Number(p.id), {
      x: p.position?.xMilli ?? 0,
      y: p.position?.yMilli ?? 0,
      activity: p.activity ?? 0,
      food: p.needs?.[NEED_FOOD] ?? 1000,
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

  const pipLayer = new Container();
  // Isometric depth: pips further along x+y are "further back" and must be
  // drawn first, or a near pip standing behind a far one on screen would be
  // wrongly occluded.
  pipLayer.sortableChildren = true;
  world.addChild(pipLayer);

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
  let lastTickAt = performance.now();
  let tickMs = 1000 / FALLBACK_TICK_HZ;
  let source = "connecting…";
  let tick = 0n;

  const applySnapshot = (next: Snapshot, atTick: bigint) => {
    prev = curr;
    curr = next;
    tick = atTick;
    lastTickAt = performance.now();
  };

  // --- served world ---------------------------------------------------------

  const runServed = async (client: WorldClient) => {
    const joined = await join(client, `web-${Math.floor(Math.random() * 1e6)}`);
    tickMs = 1000 / joined.tickHz;
    source = `${GATEWAY_URL} · seed ${joined.simSeed}`;

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
      applySnapshot(snapshotFromDelta(delta), delta.tick);
    }
  };

  // --- offline fallback -----------------------------------------------------

  const runLocal = () => {
    source = "local (no gateway)";
    const sim = new SimHandle(42n);
    const stride = pip_stride();
    const rand = mulberry32(42);

    for (let i = 0; i < FALLBACK_PIPS; i++) {
      sim.queue_spawn(
        `pip-${i}`,
        Math.floor(rand() * GRID_W) * MILLI_PER_TILE,
        Math.floor(rand() * GRID_H) * MILLI_PER_TILE,
      );
    }

    setInterval(() => {
      sim.step();
      applySnapshot(snapshotFromWasm(sim.positions(), stride), sim.tick);
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

  app.ticker.add(() => {
    // How far we are between the last authoritative tick and the next one.
    const alpha = Math.min(1, (performance.now() - lastTickAt) / tickMs);

    for (const [id, now] of curr) {
      let g = sprites.get(id);
      if (!g) {
        g = new Graphics().circle(0, 0, 4).fill(0xffffff);
        pipLayer.addChild(g);
        sprites.set(id, g);
      }

      const before = prev.get(id) ?? now;
      const tileX = toTile(before.x + (now.x - before.x) * alpha);
      const tileY = toTile(before.y + (now.y - before.y) * alpha);
      const screen = isoProject(tileX, tileY);
      g.position.set(screen.x, screen.y);
      g.zIndex = isoDepth(tileX, tileY);
      g.tint = foodColor(now.food);
      g.alpha = now.activity === ACTIVITY_WALKING ? 1 : 0.6;
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
