// Runs the same scenario as crates/sim/examples/parity.rs, but through WASM.
//
// The scenario must stay byte-identical to the Rust one — same seed, same
// spawn positions, same movement orders, same tick count. If you change one,
// change both.

import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const wasmPath = process.argv[2];
if (!wasmPath) {
  console.error("usage: node parity.mjs <path-to-nodejs-wasm-build>");
  process.exit(2);
}

const { SimHandle } = require(wasmPath);

const SEED = 42n;
const PIPS = 50;
const TICKS = 500;

const sim = new SimHandle(SEED);

for (let i = 0; i < PIPS; i++) {
  sim.queue_spawn(`pip-${i}`, (i * 137) % 48000, (i * 91) % 30000);
}
sim.step();

for (let t = 0; t < TICKS; t++) {
  if (t % 7 === 0) {
    const pip = ((t / 7) % PIPS | 0) + 1;
    sim.queue_move(pip, (t * 311) % 48000, (t * 173) % 30000);
  }
  sim.step();
}

console.log(sim.state_hash.toString(16).padStart(16, "0"));
console.error(`wasm:   tick=${sim.tick} pips=${sim.pip_count}`);
