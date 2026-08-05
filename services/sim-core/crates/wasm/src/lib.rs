//! WASM bindings for the simulation core.
//!
//! The browser runs a local copy of the world seeded identically to the server
//! and steps it forward to predict where pips will be between authoritative
//! updates. When a server delta arrives, the client reconciles.
//!
//! Because both sides run this same code with fixed-point arithmetic, the
//! prediction is exact rather than approximate — the reconciliation is a
//! correction for lost packets, not for drifting math.

use sim::{Intent, Vec2, World};
use wasm_bindgen::prelude::*;

#[wasm_bindgen]
pub struct SimHandle {
    world: World,
}

#[wasm_bindgen]
impl SimHandle {
    /// Seed must match the one the server reported in JoinWorldResponse.
    #[wasm_bindgen(constructor)]
    pub fn new(seed: u64) -> SimHandle {
        SimHandle { world: World::new(seed) }
    }

    /// Advances local prediction by one tick with no intents.
    pub fn step(&mut self) {
        self.world.step(&[]);
    }

    pub fn spawn(&mut self, name: String, x: i32, y: i32) {
        self.world.step(&[Intent::Spawn { name, position: Vec2 { x, y } }]);
    }

    pub fn move_pip(&mut self, pip: u64, x: i32, y: i32) {
        self.world.step(&[Intent::Move { pip, destination: Vec2 { x, y } }]);
    }

    #[wasm_bindgen(getter)]
    pub fn tick(&self) -> u64 {
        self.world.tick
    }

    /// Compare against the server's hash to detect that prediction has drifted
    /// and a full resync is needed.
    #[wasm_bindgen(getter)]
    pub fn state_hash(&self) -> u64 {
        self.world.state_hash()
    }

    /// Flat [id, x, y, activity] quadruples — a typed array the renderer can
    /// read without allocating one JS object per pip every frame.
    pub fn positions(&self) -> Vec<i32> {
        let mut out = Vec::with_capacity(self.world.len() * 4);
        for i in 0..self.world.len() {
            out.push(self.world.ids[i] as i32);
            out.push(self.world.positions[i].x);
            out.push(self.world.positions[i].y);
            out.push(self.world.activities[i] as i32);
        }
        out
    }
}
