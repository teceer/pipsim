import { defineConfig } from "vite";

export default defineConfig({
  server: { port: 5173 },
  // The WASM module is emitted by `make -C services/sim-core wasm` into
  // src/sim-wasm/. It is gitignored — a build artifact, not source.
  optimizeDeps: { exclude: ["./src/sim-wasm/sim_wasm.js"] },
});
