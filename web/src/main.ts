/**
 * Browser client.
 *
 * The rendering loop runs at display refresh rate; the server is authoritative
 * at 10 Hz. The gap is filled by running the *same* simulation core locally,
 * compiled to WASM from services/sim-core. Prediction is therefore exact rather
 * than approximate — reconciliation corrects for lost updates, not for drifting
 * arithmetic between two implementations of the same rules.
 */

import { Application, Container, Graphics } from "pixi.js";

// Built by `make -C services/sim-core wasm`.
// import init, { SimHandle } from "./sim-wasm/sim_wasm.js";

const TILE = 32;
const MILLI_PER_TILE = 1000;

async function main() {
  const app = new Application();
  await app.init({ background: "#11131a", resizeTo: window, antialias: true });
  document.body.appendChild(app.canvas);

  const world = new Container();
  app.stage.addChild(world);

  // TODO: JoinWorld over Connect, then seed the local WASM sim with the seed
  // the server reports so prediction starts from an identical state.
  // TODO: StreamWorld server stream -> reconcile against local prediction.

  const sprites = new Map<number, Graphics>();

  app.ticker.add(() => {
    // TODO: step the local WASM sim and read back the flat
    // [id, x, y, activity] array, which avoids allocating one object per pip
    // per frame.
    for (const [, g] of sprites) {
      g.visible = true;
    }
  });

  // Placeholder so there is something on screen before the wiring exists.
  const marker = new Graphics().circle(0, 0, 6).fill("#7dd3fc");
  marker.position.set((4 * MILLI_PER_TILE * TILE) / MILLI_PER_TILE, 4 * TILE);
  world.addChild(marker);
}

main().catch((err) => console.error(err));
