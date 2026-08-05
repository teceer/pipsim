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

/// World bounds in milli-tiles. The renderer's grid must match these, or pips
/// will walk off the drawn area.
pub const WORLD_W_MILLI: Milli = 48_000;
pub const WORLD_H_MILLI: Milli = 30_000;

/// Chance per tick that an idle pip decides to go somewhere, in parts per
/// thousand. At 10 Hz, 15/1000 means an idle pip sets off roughly every seven
/// seconds — busy enough to look alive, sparse enough that the event log stays
/// readable.
pub const WANDER_CHANCE_PER_MILLE: i32 = 15;

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
        Vec2 {
            x: self.x + dx,
            y: self.y + dy,
        }
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
        Needs {
            food: NEED_MAX,
            rest: NEED_MAX,
            social: NEED_MAX,
        }
    }
}

/// Intents are queued and applied at the START of a tick, never mid-tick.
/// Applying them as they arrive would make the outcome depend on network
/// timing, which is exactly what determinism forbids.
#[derive(Clone, Debug)]
pub enum Intent {
    Spawn {
        name: String,
        position: Vec2,
    },
    Move {
        pip: PipId,
        destination: Vec2,
    },
    Hire {
        pip: PipId,
        workplace: WorkplaceId,
    },
    /// The effect of a shift, handed back by a workplace service.
    ///
    /// Workplaces decide what work costs and gives; the core decides what that
    /// means for the pip. Deltas are clamped here, so a buggy or hostile
    /// workplace cannot push a need outside its range or resurrect the dead.
    ApplyNeeds {
        pip: PipId,
        food: i32,
        rest: i32,
        social: i32,
    },
}

pub type PipId = u64;
pub type WorkplaceId = u64;

/// Facts produced by a tick. The core does not publish them — `server` picks
/// them up and writes them to Kafka.
#[derive(Clone, Debug, PartialEq)]
pub enum DomainEvent {
    PipSpawned {
        pip: PipId,
        name: String,
        position: Vec2,
    },
    PipStartedWork {
        pip: PipId,
        workplace: WorkplaceId,
    },
    PipGotHungry {
        pip: PipId,
        food_level: i32,
    },
    PipDied {
        pip: PipId,
        cause: &'static str,
    },
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
        self.wander();
        self.move_walkers();
        self.tick += 1;

        events
    }

    /// Idle pips pick somewhere to go.
    ///
    /// This belongs in the core rather than in a client or a driver script: it
    /// is a rule about how pips behave, and putting it anywhere else would mean
    /// the browser's prediction and the server's world disagree about what
    /// happens next. Because the roll comes from the seeded `Rng` carried in
    /// `World`, wandering stays fully reproducible.
    ///
    /// The order matters: every pip is offered a roll on every tick, in index
    /// order, so the RNG is consumed identically regardless of how many pips
    /// happen to be idle.
    fn wander(&mut self) {
        for i in 0..self.positions.len() {
            let roll = self.rng.next_range(0, 1000);
            if self.activities[i] != Activity::Idle {
                continue;
            }
            if roll >= WANDER_CHANCE_PER_MILLE {
                continue;
            }

            let x = self.rng.next_range(0, WORLD_W_MILLI);
            let y = self.rng.next_range(0, WORLD_H_MILLI);
            self.destinations[i] = Some(Vec2 { x, y });
            self.activities[i] = Activity::Walking;
        }
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
                    self.needs.push(Needs {
                        food: NEED_MAX - jitter,
                        ..Needs::full()
                    });
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
                        self.activities[i] = Activity::Working;
                        self.destinations[i] = None;
                        events.push(DomainEvent::PipStartedWork {
                            pip: *pip,
                            workplace: *workplace,
                        });
                    }
                }
                Intent::ApplyNeeds {
                    pip,
                    food,
                    rest,
                    social,
                } => {
                    if let Some(i) = self.index_of(*pip) {
                        let n = &mut self.needs[i];
                        // Clamped, not trusted. A workplace is a separate
                        // service and may be wrong; the core owns the invariant
                        // that needs stay in range.
                        n.food = (n.food + food).clamp(0, NEED_MAX);
                        n.rest = (n.rest + rest).clamp(0, NEED_MAX);
                        n.social = (n.social + social).clamp(0, NEED_MAX);
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
            events.push(DomainEvent::PipDied {
                pip: self.ids[i],
                cause: "starvation",
            });
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
            let Some(dest) = self.destinations[i] else {
                continue;
            };

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
                position: Vec2 {
                    x: i as Milli * 100,
                    y: 0,
                },
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
                    vec![Intent::Move {
                        pip: (t / 25 + 1) as PipId,
                        destination: Vec2 { x: 9000, y: 9000 },
                    }]
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

        assert_eq!(
            hungry, 3,
            "each pip crosses the hunger threshold exactly once"
        );
        assert_eq!(died, 3);
        assert!(w.is_empty());
    }

    #[test]
    fn walking_reaches_its_destination() {
        let mut w = World::new(3);
        w.step(&spawn(1));
        let dest = Vec2 { x: 500, y: 500 };
        w.step(&[Intent::Move {
            pip: 1,
            destination: dest,
        }]);

        // Arrival is the property under test, not staying put. Once a pip is
        // idle again `wander` may send it somewhere else, so asserting on the
        // final position would amount to asserting that wandering does not
        // exist.
        let mut arrived = false;
        for _ in 0..100 {
            w.step(&[]);
            if w.positions[0] == dest {
                arrived = true;
                break;
            }
        }

        assert!(arrived, "pip never reached its destination");
    }

    #[test]
    fn idle_pips_start_wandering_and_stay_in_bounds() {
        let mut w = World::new(11);
        w.step(&spawn(30));

        for _ in 0..300 {
            w.step(&[]);
        }

        let walking = w
            .activities
            .iter()
            .filter(|a| **a == Activity::Walking)
            .count();
        assert!(walking > 0, "nobody ever set off");

        for p in &w.positions {
            assert!(
                (0..=WORLD_W_MILLI).contains(&p.x),
                "x out of bounds: {}",
                p.x
            );
            assert!(
                (0..=WORLD_H_MILLI).contains(&p.y),
                "y out of bounds: {}",
                p.y
            );
        }
    }

    /// Wandering must not break the property everything else rests on.
    #[test]
    fn wandering_is_reproducible() {
        let run = || {
            let mut w = World::new(99);
            w.step(&spawn(40));
            for _ in 0..400 {
                w.step(&[]);
            }
            w
        };
        assert_eq!(run().state_hash(), run().state_hash());
    }

    #[test]
    fn work_effects_are_clamped_to_the_needs_range() {
        let mut w = World::new(5);
        w.step(&spawn(1));

        // A workplace handing back an absurd number must not push a need out of
        // range. The core owns this invariant precisely because the workplace is
        // a separate service that may be wrong.
        //
        // Note the need is checked after a full step, so the per-tick drain has
        // already been applied — the invariant is the ceiling, not equality.
        w.step(&[Intent::ApplyNeeds {
            pip: 1,
            food: 999_999,
            rest: 0,
            social: 0,
        }]);
        assert!(w.needs[0].food <= NEED_MAX);
        assert!(
            w.needs[0].food > NEED_MAX - 10,
            "the meal should have landed"
        );

        // Draining below zero is starvation, and starvation is fatal — the
        // clamp keeps the number in range, it does not keep the pip alive.
        w.step(&[Intent::ApplyNeeds {
            pip: 1,
            food: -999_999,
            rest: 0,
            social: 0,
        }]);
        assert!(w.is_empty(), "a pip drained to nothing should have died");
    }

    /// The loop the farm exists to close: a starving pip fed by work survives.
    #[test]
    fn being_fed_prevents_starvation() {
        let mut w = World::new(6);
        w.step(&spawn(1));

        for t in 0..3000 {
            let intents = if t % 100 == 0 {
                vec![Intent::ApplyNeeds {
                    pip: 1,
                    food: 300,
                    rest: 0,
                    social: 0,
                }]
            } else {
                vec![]
            };
            w.step(&intents);
        }

        assert_eq!(w.len(), 1, "a regularly fed pip should still be alive");
    }

    #[test]
    fn hiring_emits_started_work() {
        let mut w = World::new(9);
        w.step(&spawn(1));
        let events = w.step(&[Intent::Hire {
            pip: 1,
            workplace: 77,
        }]);

        assert!(events.contains(&DomainEvent::PipStartedWork {
            pip: 1,
            workplace: 77
        }));
        assert_eq!(w.employers[0], Some(77));
    }
}
