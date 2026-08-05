//! Deterministic simulation core.
//!
//! Invariants enforced by tests in this crate:
//!
//! 1. `step` is a pure function of `(state, intents, tick)`. No clock, no
//!    global RNG, no I/O.
//! 2. Iteration order is stable. Entities live in `Vec`s indexed by a dense
//!    slot; there is no `HashMap` anywhere in the hot path, because its
//!    randomized seed makes iteration order differ between runs.
//! 3. Positions and needs are fixed-point integers. Floats would let native
//!    and WASM builds diverge, which would silently break client prediction.
//!
//! Data layout is structure-of-arrays: `positions[i]` and `needs[i]` describe
//! the same pip. That is what keeps the tick loop cache-friendly, and it is the
//! reason this is not an actor-per-entity design.

#![forbid(unsafe_code)]

pub mod rng;

use rng::Rng;

/// Milli-tiles. 1000 == one tile.
pub type Milli = i32;

pub const NEED_MAX: i32 = 1000;
pub const FOOD_HUNGRY_THRESHOLD: i32 = 300;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Vec2 {
    pub x: Milli,
    pub y: Milli,
}

impl Vec2 {
    pub const ZERO: Vec2 = Vec2 { x: 0, y: 0 };

    /// Integer-only Chebyshev-ish step towards `target`, capped at `speed`.
    /// Avoids sqrt precisely because float results are not reproducible
    /// across targets.
    pub fn step_towards(self, target: Vec2, speed: Milli) -> Vec2 {
        let dx = (target.x - self.x).clamp(-speed, speed);
        let dy = (target.y - self.y).clamp(-speed, speed);
        Vec2 { x: self.x + dx, y: self.y + dy }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Activity {
    Idle,
    Walking,
    Working,
    Eating,
    Sleeping,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Needs {
    pub food: i32,
    pub rest: i32,
    pub social: i32,
}

impl Needs {
    pub fn full() -> Self {
        Needs { food: NEED_MAX, rest: NEED_MAX, social: NEED_MAX }
    }
}

/// Intents are queued and applied at the START of a tick, never mid-tick.
/// Applying them as they arrive would make the outcome depend on network
/// timing, which is exactly what determinism forbids.
#[derive(Clone, Debug)]
pub enum Intent {
    Spawn { name: String, position: Vec2 },
    Move { pip: PipId, destination: Vec2 },
    Hire { pip: PipId, workplace: WorkplaceId },
}

pub type PipId = u64;
pub type WorkplaceId = u64;

/// Facts produced by a tick. The core does not publish them — `server` picks
/// them up and writes them to Kafka.
#[derive(Clone, Debug, PartialEq)]
pub enum DomainEvent {
    PipSpawned { pip: PipId, name: String, position: Vec2 },
    PipStartedWork { pip: PipId, workplace: WorkplaceId },
    PipGotHungry { pip: PipId, food_level: i32 },
    PipDied { pip: PipId, cause: &'static str },
}

/// Structure-of-arrays world state.
#[derive(Clone, Debug)]
pub struct World {
    pub tick: u64,
    rng: Rng,

    // Parallel arrays — index `i` is the same pip in all of them.
    pub ids: Vec<PipId>,
    pub names: Vec<String>,
    pub positions: Vec<Vec2>,
    pub destinations: Vec<Option<Vec2>>,
    pub activities: Vec<Activity>,
    pub needs: Vec<Needs>,
    pub employers: Vec<Option<WorkplaceId>>,

    next_pip_id: PipId,
}

impl World {
    pub fn new(seed: u64) -> Self {
        World {
            tick: 0,
            rng: Rng::new(seed),
            ids: Vec::new(),
            names: Vec::new(),
            positions: Vec::new(),
            destinations: Vec::new(),
            activities: Vec::new(),
            needs: Vec::new(),
            employers: Vec::new(),
            next_pip_id: 1,
        }
    }

    pub fn len(&self) -> usize {
        self.ids.len()
    }

    pub fn is_empty(&self) -> bool {
        self.ids.is_empty()
    }

    /// Linear scan on purpose: at the scale this runs at, it beats a HashMap
    /// on cache behaviour and keeps iteration order deterministic.
    fn index_of(&self, pip: PipId) -> Option<usize> {
        self.ids.iter().position(|&id| id == pip)
    }

    /// Advances the world by exactly one tick.
    ///
    /// This is the only mutating entry point, and it is a pure function of
    /// `(self, intents)`. Given the same seed and the same intent sequence it
    /// produces the same world and the same events, natively and in WASM.
    pub fn step(&mut self, intents: &[Intent]) -> Vec<DomainEvent> {
        let mut events = Vec::new();

        self.apply_intents(intents, &mut events);
        self.decay_needs(&mut events);
        self.move_walkers();
        self.tick += 1;

        events
    }

    fn apply_intents(&mut self, intents: &[Intent], events: &mut Vec<DomainEvent>) {
        for intent in intents {
            match intent {
                Intent::Spawn { name, position } => {
                    let id = self.next_pip_id;
                    self.next_pip_id += 1;

                    self.ids.push(id);
                    self.names.push(name.clone());
                    self.positions.push(*position);
                    self.destinations.push(None);
                    self.activities.push(Activity::Idle);
                    // Jitter starting needs so a freshly spawned cohort does not
                    // move in lockstep forever. Seeded, so still reproducible.
                    let jitter = self.rng.next_range(0, 200);
                    self.needs.push(Needs { food: NEED_MAX - jitter, ..Needs::full() });
                    self.employers.push(None);

                    events.push(DomainEvent::PipSpawned {
                        pip: id,
                        name: name.clone(),
                        position: *position,
                    });
                }
                Intent::Move { pip, destination } => {
                    if let Some(i) = self.index_of(*pip) {
                        self.destinations[i] = Some(*destination);
                        self.activities[i] = Activity::Walking;
                    }
                }
                Intent::Hire { pip, workplace } => {
                    if let Some(i) = self.index_of(*pip) {
                        self.employers[i] = Some(*workplace);
                        events.push(DomainEvent::PipStartedWork {
                            pip: *pip,
                            workplace: *workplace,
                        });
                    }
                }
            }
        }
    }

    fn decay_needs(&mut self, events: &mut Vec<DomainEvent>) {
        let mut died = Vec::new();

        for i in 0..self.needs.len() {
            let was_hungry = self.needs[i].food <= FOOD_HUNGRY_THRESHOLD;

            let drain = match self.activities[i] {
                Activity::Working => 2,
                Activity::Walking => 1,
                _ => 1,
            };
            self.needs[i].food -= drain;

            if self.needs[i].food <= 0 {
                died.push(i);
                continue;
            }
            if !was_hungry && self.needs[i].food <= FOOD_HUNGRY_THRESHOLD {
                events.push(DomainEvent::PipGotHungry {
                    pip: self.ids[i],
                    food_level: self.needs[i].food,
                });
            }
        }

        // Remove back to front so earlier indices stay valid. `swap_remove`
        // would be faster but reorders the arrays, and stable ordering is worth
        // more here than the constant factor.
        for &i in died.iter().rev() {
            events.push(DomainEvent::PipDied { pip: self.ids[i], cause: "starvation" });
            self.remove_at(i);
        }
    }

    fn remove_at(&mut self, i: usize) {
        self.ids.remove(i);
        self.names.remove(i);
        self.positions.remove(i);
        self.destinations.remove(i);
        self.activities.remove(i);
        self.needs.remove(i);
        self.employers.remove(i);
    }

    fn move_walkers(&mut self) {
        const SPEED: Milli = 50;

        for i in 0..self.positions.len() {
            let Some(dest) = self.destinations[i] else { continue };

            self.positions[i] = self.positions[i].step_towards(dest, SPEED);
            if self.positions[i] == dest {
                self.destinations[i] = None;
                self.activities[i] = Activity::Idle;
            }
        }
    }

    /// Order-sensitive hash of the whole world. Replay compares this tick by
    /// tick to find the exact moment two runs diverged.
    pub fn state_hash(&self) -> u64 {
        let mut h: u64 = 0xcbf29ce484222325;
        let mut mix = |v: u64| {
            h ^= v;
            h = h.wrapping_mul(0x100000001b3);
        };

        mix(self.tick);
        for i in 0..self.ids.len() {
            mix(self.ids[i]);
            mix(self.positions[i].x as u64);
            mix(self.positions[i].y as u64);
            mix(self.needs[i].food as u64);
            mix(self.activities[i] as u64);
        }
        h
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn spawn(n: usize) -> Vec<Intent> {
        (0..n)
            .map(|i| Intent::Spawn {
                name: format!("pip-{i}"),
                position: Vec2 { x: i as Milli * 100, y: 0 },
            })
            .collect()
    }

    /// The invariant the entire replay story rests on.
    #[test]
    fn same_seed_and_intents_produce_identical_worlds() {
        let run = || {
            let mut w = World::new(42);
            w.step(&spawn(50));
            for t in 0..500 {
                let intents = if t % 25 == 0 {
                    vec![Intent::Move { pip: (t / 25 + 1) as PipId, destination: Vec2 { x: 9000, y: 9000 } }]
                } else {
                    vec![]
                };
                w.step(&intents);
            }
            w
        };

        let a = run();
        let b = run();

        assert_eq!(a.state_hash(), b.state_hash());
        assert_eq!(a.positions, b.positions);
        assert_eq!(a.tick, b.tick);
    }

    #[test]
    fn different_seeds_diverge() {
        let mut a = World::new(1);
        let mut b = World::new(2);
        a.step(&spawn(20));
        b.step(&spawn(20));
        assert_ne!(a.state_hash(), b.state_hash());
    }

    #[test]
    fn pips_starve_and_emit_events() {
        let mut w = World::new(7);
        w.step(&spawn(3));

        let mut hungry = 0;
        let mut died = 0;
        for _ in 0..2000 {
            for e in w.step(&[]) {
                match e {
                    DomainEvent::PipGotHungry { .. } => hungry += 1,
                    DomainEvent::PipDied { .. } => died += 1,
                    _ => {}
                }
            }
        }

        assert_eq!(hungry, 3, "each pip crosses the hunger threshold exactly once");
        assert_eq!(died, 3);
        assert!(w.is_empty());
    }

    #[test]
    fn walking_reaches_destination_and_stops() {
        let mut w = World::new(3);
        w.step(&spawn(1));
        let dest = Vec2 { x: 500, y: 500 };
        w.step(&[Intent::Move { pip: 1, destination: dest }]);

        for _ in 0..100 {
            w.step(&[]);
        }

        assert_eq!(w.positions[0], dest);
        assert_eq!(w.activities[0], Activity::Idle);
        assert_eq!(w.destinations[0], None);
    }

    #[test]
    fn hiring_emits_started_work() {
        let mut w = World::new(9);
        w.step(&spawn(1));
        let events = w.step(&[Intent::Hire { pip: 1, workplace: 77 }]);

        assert!(events.contains(&DomainEvent::PipStartedWork { pip: 1, workplace: 77 }));
        assert_eq!(w.employers[0], Some(77));
    }
}
