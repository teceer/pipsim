/**
 * Browser client.
 *
 * The architectural point of this file: the rendering loop runs at display
 * refresh rate while the simulation is authoritative at 10 Hz, and the gap is
 * filled by running the *same* simulation core locally, compiled to WASM from
 * services/sim-core.
 *
 * That is why prediction here is exact rather than approximate. There is no
 * second implementation of the movement rules to drift out of sync with the
 * server's — reconciliation exists to correct for lost updates, not for
 * arithmetic disagreeing between two codebases.
 *
 * The world is currently driven locally so there is something to look at before
 * world-gateway exists. Once it does, only two things change here: the seed
 * comes from JoinWorld instead of a constant, and server deltas replace the
 * locally generated intents.
 */

import { Application, Container, Graphics, Text } from "pixi.js";
import init, { SimHandle, pip_stride } from "./sim-wasm/sim_wasm.js";

// --- world constants --------------------------------------------------------

const MILLI_PER_TILE = 1000;

const GRID_W = 48;
const GRID_H = 30;

/**
 * 2:1 isometric tile diamond, in pixels. Width is the full diagonal of the
 * diamond, height is half of that — the ratio that keeps the tile art
 * artifact-free at integer scales.
 */
const ISO_TILE_W = 32;
const ISO_TILE_H = 16;

/** Must match the server's SIM_TICK_HZ. */
const TICK_HZ = 10;
const TICK_MS = 1000 / TICK_HZ;

const PIP_COUNT = 300;

/** Mirrors sim::Activity. */
const ACTIVITY_WALKING = 1;

// --- deterministic client-side randomness -----------------------------------

/**
 * The client picks destinations only because there is no gateway yet, and it
 * does so from a seeded generator so a reload replays the same run. Anything
 * affecting world state has to be reproducible — that rule does not stop at
 * the WASM boundary.
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

function snapshot(flat: Int32Array, stride: number): Snapshot {
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

  // This will come from JoinWorldResponse once the gateway exists. Both sides
  // must start from the same number or prediction is meaningless.
  const sim = new SimHandle(42n);
  const stride = pip_stride();
  const rand = mulberry32(42);

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

  // Seed the population. These are queued as intents and applied at the next
  // tick boundary, exactly the way the server would apply them.
  for (let i = 0; i < PIP_COUNT; i++) {
    sim.queue_spawn(
      `pip-${i}`,
      Math.floor(rand() * GRID_W) * MILLI_PER_TILE,
      Math.floor(rand() * GRID_H) * MILLI_PER_TILE,
    );
  }

  const sprites = new Map<number, Graphics>();
  let prev: Snapshot = new Map();
  let curr: Snapshot = snapshot(sim.positions(), stride);
  let lastTickAt = performance.now();

  // Backdrop so the HUD stays readable once pips wander underneath it.
  const hudPanel = new Graphics()
    .roundRect(8, 8, 268, 128, 6)
    .fill({ color: 0x0b0d12, alpha: 0.82 });
  app.stage.addChild(hudPanel);

  const hud = new Text({
    text: "",
    style: { fill: "#94a3b8", fontFamily: "monospace", fontSize: 13 },
  });
  hud.position.set(20, 18);
  app.stage.addChild(hud);

  // --- simulation: 10 Hz, deliberately independent of frame rate ------------

  setInterval(() => {
    // Hand new destinations to whoever is standing around. In the real system
    // these arrive from the gateway as intents.
    for (const [id, p] of curr) {
      if (p.activity !== ACTIVITY_WALKING && rand() < 0.05) {
        sim.queue_move(
          id,
          Math.floor(rand() * GRID_W) * MILLI_PER_TILE,
          Math.floor(rand() * GRID_H) * MILLI_PER_TILE,
        );
      }
    }

    sim.step();

    prev = curr;
    curr = snapshot(sim.positions(), stride);
    lastTickAt = performance.now();
  }, TICK_MS);

  // --- rendering: display refresh rate, interpolated between ticks -----------

  app.ticker.add(() => {
    // How far we are between the last authoritative tick and the next one.
    const alpha = Math.min(1, (performance.now() - lastTickAt) / TICK_MS);

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

    // Pips that starved since the previous tick.
    for (const [id, g] of sprites) {
      if (!curr.has(id)) {
        g.destroy();
        sprites.delete(id);
      }
    }

    hud.text = [
      `tick   ${sim.tick}`,
      `pips   ${sim.pip_count}`,
      `hash   ${sim.state_hash.toString(16).padStart(16, "0")}`,
      `fps    ${app.ticker.FPS.toFixed(0)}`,
      "",
      `sim ${TICK_HZ} Hz — interpolated to display rate`,
    ].join("\n");
  });
}

main().catch((err) => {
  console.error(err);
  document.body.innerHTML = `<pre style="color:#f87171;padding:2rem;font-family:monospace">${err}</pre>`;
});
