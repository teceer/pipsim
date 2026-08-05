//! WASM bindings for the simulation core.
//!
//! The browser runs a local copy of the world, seeded identically to the
//! server, and steps it forward to predict where pips will be between
//! authoritative updates. When a server delta arrives, the client reconciles.
//!
//! Because both sides run this same code with fixed-point arithmetic, the
//! prediction is exact rather than approximate — reconciliation corrects for
//! lost updates, not for two implementations of the same rules drifting apart.
//!
//! This handle deliberately mirrors the server's tick driver: intents are
//! queued and drained at the tick boundary, never applied on arrival. Applying
//! them mid-tick would make the outcome depend on timing, which is exactly what
//! determinism forbids — and would make the client's prediction diverge from
//! the server's for reasons that are very hard to see.

use sim::{Activity, Intent, Vec2, World};
use wasm_bindgen::prelude::*;

/// Stride of the array returned by `positions()`: id, x, y, activity, food.
pub const PIP_STRIDE: usize = 5;

#[wasm_bindgen]
pub struct SimHandle {
    world: World,
    pending: Vec<Intent>,
}

#[wasm_bindgen]
impl SimHandle {
    /// `seed` must match the one the server reported in `JoinWorldResponse`,
    /// or prediction starts from a different world and every frame is wrong.
    #[wasm_bindgen(constructor)]
    pub fn new(seed: u64) -> SimHandle {
        SimHandle {
            world: World::new(seed),
            pending: Vec::new(),
        }
    }

    /// Queues a spawn for the next tick.
    pub fn queue_spawn(&mut self, name: String, x: i32, y: i32) {
        self.pending.push(Intent::Spawn {
            name,
            position: Vec2 { x, y },
        });
    }

    /// Queues a movement order for the next tick.
    pub fn queue_move(&mut self, pip: u32, x: i32, y: i32) {
        self.pending.push(Intent::Move {
            pip: pip as u64,
            destination: Vec2 { x, y },
        });
    }

    pub fn queue_hire(&mut self, pip: u32, workplace: u32) {
        self.pending.push(Intent::Hire {
            pip: pip as u64,
            workplace: workplace as u64,
        });
    }

    /// Advances the world by exactly one tick, draining queued intents first.
    /// Returns how many domain events the tick produced — the client uses this
    /// only for display; the authoritative copies come from the server.
    pub fn step(&mut self) -> usize {
        let intents = std::mem::take(&mut self.pending);
        self.world.step(&intents).len()
    }

    #[wasm_bindgen(getter)]
    pub fn tick(&self) -> u64 {
        self.world.tick
    }

    #[wasm_bindgen(getter)]
    pub fn pip_count(&self) -> usize {
        self.world.len()
    }

    /// Compare against the server's hash to detect that prediction has drifted
    /// far enough to need a full resync rather than a delta.
    #[wasm_bindgen(getter)]
    pub fn state_hash(&self) -> u64 {
        self.world.state_hash()
    }

    /// Flat `[id, x, y, activity, food]` tuples as a single `Int32Array`.
    ///
    /// Flat rather than an array of objects on purpose: at 60 fps with hundreds
    /// of pips, allocating one JS object per pip per frame is what would
    /// actually make this stutter, not the simulation.
    pub fn positions(&self) -> Vec<i32> {
        let mut out = Vec::with_capacity(self.world.len() * PIP_STRIDE);
        for i in 0..self.world.len() {
            out.push(self.world.ids[i] as i32);
            out.push(self.world.positions[i].x);
            out.push(self.world.positions[i].y);
            out.push(activity_code(self.world.activities[i]));
            out.push(self.world.needs[i].food);
        }
        out
    }
}

fn activity_code(a: Activity) -> i32 {
    match a {
        Activity::Idle => 0,
        Activity::Walking => 1,
        Activity::Working => 2,
        Activity::Eating => 3,
        Activity::Sleeping => 4,
    }
}

/// Stride exposed to JS so the renderer never hardcodes it.
#[wasm_bindgen]
pub fn pip_stride() -> usize {
    PIP_STRIDE
}
