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

/// One point of social is lost every this many ticks. At 10 Hz that is a full
/// bar in a little over an hour of wall clock — slow enough to be background,
/// fast enough that a tavern shift is worth walking to.
pub const SOCIAL_DECAY_EVERY: u64 = 4;

/// World bounds in milli-tiles. The renderer's grid must match these, or pips
/// will walk off the drawn area.
pub const WORLD_W_MILLI: Milli = 48_000;
pub const WORLD_H_MILLI: Milli = 30_000;

/// Chance per tick that an idle pip decides to go somewhere, in parts per
/// thousand. At 10 Hz, 15/1000 means an idle pip sets off roughly every seven
/// seconds — busy enough to look alive, sparse enough that the event log stays
/// readable.
pub const WANDER_CHANCE_PER_MILLE: i32 = 15;

/// Milli-tiles per tick. At 10 Hz that is 1.5 tiles a second.
///
/// Raised from 50 when pips started walking to work instead of teleporting
/// there. The world is 48 tiles wide, so the worst-case commute at the old
/// speed was 72 seconds of unpaid walking against a food drain of one per
/// tick — employment would have killed more pips than it fed.
pub const WALK_SPEED_MILLI: Milli = 150;

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

    /// Squared distance to `other`. Compared against a squared threshold
    /// rather than taking a square root, for the same reason `step_towards`
    /// avoids one: `sqrt` is not guaranteed bit-identical between native and
    /// WASM, and determinism cannot tolerate that.
    fn distance_squared(self, other: Vec2) -> i64 {
        let dx = (self.x - other.x) as i64;
        let dy = (self.y - other.y) as i64;
        dx * dx + dy * dy
    }
}

/// How close two buildings may stand. A placeholder for real building
/// footprints — sim-core has no notion of building size yet — but it is
/// enough to catch the case that actually matters: two workplaces registered
/// on top of each other, which used to be possible because nothing checked.
pub const WORKPLACE_MIN_SPACING_MILLI: Milli = 1000;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Activity {
    Idle,
    Walking,
    Working,
    Eating,
    Sleeping,
    /// Employed, but not in the building yet — either still walking there or
    /// queuing at the door because it is full.
    Commuting,
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

/// What a workplace can sell or produce, mirrored from
/// `pips.workplace.v1.ResourceKind` (this crate takes no dependencies, so the
/// wire enum is not available here — `crates/server` maps between the two).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ResourceKind {
    Grain,
    Food,
    Tool,
    Ale,
}

/// What one unit of a resource does to a pip. The single owner of "what a
/// resource does to a pip" — ADR 0006. `ALE` restores social everywhere in
/// the world, because that is what ale is; a workplace declares that it
/// *sells* ale, never what ale does.
///
/// Grain and Tool have no direct effect on a pip: they are inputs a workplace
/// consumes to produce something else, not something a pip drinks or eats.
pub fn need_effects(kind: ResourceKind) -> Needs {
    match kind {
        ResourceKind::Grain => Needs {
            food: 0,
            rest: 0,
            social: 0,
        },
        ResourceKind::Food => Needs {
            food: 200,
            rest: 0,
            social: 0,
        },
        ResourceKind::Tool => Needs {
            food: 0,
            rest: 0,
            social: 0,
        },
        ResourceKind::Ale => Needs {
            food: 0,
            rest: 0,
            social: 150,
        },
    }
}

/// Why money is moving. Mirrors `pips.sim.v1.TransferKind`.
///
/// Load-bearing rather than descriptive: `Issuance` is what exempts the
/// treasury from the balance check, and that privilege is carried by the
/// transfer instead of inferred from who the payer is. Without it, every
/// movement out of the treasury could mint.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum TransferKind {
    Unspecified,
    Wage,
    Purchase,
    Issuance,
    Escheat,
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
    /// Puts a building on the map, or updates one already there.
    ///
    /// The core does not invent workplaces: each one is a service, and the
    /// gateway registers it from what `Describe` reports. `capacity` therefore
    /// arrives from the workplace rather than being decided here — the number
    /// has one owner, and the core only enforces it physically.
    RegisterWorkplace {
        /// What the building sells, and for how much. The price has one
        /// owner — the workplace service — and arrives here as a copy, the
        /// same way `capacity` does.
        sells: Vec<(ResourceKind, i64)>,
        id: WorkplaceId,
        kind: String,
        position: Vec2,
        capacity: u32,
    },
    /// Puts a service on the map, or updates the one already there.
    ///
    /// Inert: nothing in the tick loop reads a structure. It exists so the map
    /// can show what is deployed, and so that a pip can one day walk to the
    /// bank rather than being paid by an invisible hand. See ADR 0011.
    RegisterStructure {
        id: StructureId,
        kind: String,
        position: Vec2,
        role: String,
        healthy: bool,
    },
    /// The pip is no longer employed there. Frees its place in the building.
    EndEmployment {
        pip: PipId,
    },
    /// Moves money between two accounts the core knows about — a pip, a
    /// workplace, or the treasury.
    ///
    /// The bank is authoritative for history and solvency; this is the
    /// replica update that lets a tick decide "can this pip afford it"
    /// without a network call. Rejected silently (no balance moves) if the
    /// payer cannot afford it — the core is never behind on spending, only on
    /// income not yet applied.
    ///
    /// `resource_kind` set means the payer is buying that resource: after the
    /// balance moves, the core also runs `Consume` for the payer in the same
    /// intent, one unit's worth.
    Transfer {
        payer: AccountId,
        payee: AccountId,
        amount: i64,
        resource_kind: Option<ResourceKind>,
        kind: TransferKind,
    },
    /// Payroll, batched. A workplace's shift population is paid in one
    /// intent carrying every pip's credit, rather than one intent per pip —
    /// the number of pips employed does not get to set the tick budget.
    /// Rejected as a whole (no balance moves at all) if the payer cannot
    /// cover the sum of credits.
    CreditBalances {
        payer: WorkplaceId,
        credits: Vec<(PipId, i64)>,
    },
}

pub type PipId = u64;
pub type WorkplaceId = u64;
/// Ids live in their own space, not shared with workplaces: a structure is
/// registered by the gateway from configuration, a workplace by what a service
/// reports about itself, and nothing on the map addresses one expecting the
/// other.
pub type StructureId = u64;

/// A bank account, namespaced by holder kind exactly like
/// `pips.bank.v1.Account.id` — `"pip:412"` and `"workplace:3"` cannot
/// collide. Parsed once at the edge (`crates/server`) so `crates/sim` never
/// deals with strings during a tick.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum AccountId {
    Pip(PipId),
    Workplace(WorkplaceId),
    Treasury,
}

/// Facts produced by a tick. The core does not publish them — `server` picks
/// them up and writes them to Kafka.
#[derive(Clone, Debug, PartialEq)]
pub enum DomainEvent {
    PipSpawned {
        pip: PipId,
        name: String,
        position: Vec2,
    },
    /// Emitted when the pip is actually inside and working — not when it was
    /// hired. Between the two it is walking, and a fact log that claimed
    /// otherwise would misreport where everyone was.
    PipStartedWork {
        pip: PipId,
        workplace: WorkplaceId,
    },
    PipEndedWork {
        pip: PipId,
        workplace: WorkplaceId,
        reason: &'static str,
    },
    WorkplaceBuilt {
        workplace: WorkplaceId,
        kind: String,
        position: Vec2,
        capacity: u32,
    },
    PipGotHungry {
        pip: PipId,
        food_level: i32,
    },
    PipDied {
        pip: PipId,
        cause: &'static str,
    },
    /// A pip bought something, and the core is the authority that says so.
    ///
    /// The bank books this rather than deciding it. A purchase is settled
    /// against the pip's replica balance inside a tick, which is the one
    /// place the decision can be made without a network call — so the core
    /// gates it and the ledger records what happened. Emitted only when the
    /// transfer actually applied.
    PurchaseMade {
        pip: PipId,
        workplace: WorkplaceId,
        amount: i64,
        resource: ResourceKind,
    },
    /// A `RegisterWorkplace` intent was refused: off the map, or on top of
    /// another building (ADR 0008). Not a Kafka fact — rule 2 in `CLAUDE.md`
    /// reserves Kafka for things many independent consumers might care about,
    /// and this concerns exactly one audience: whoever misconfigured a
    /// position. `crates/server` turns it into a log line and a counter
    /// instead of an envelope.
    WorkplaceRegistrationRejected {
        workplace: WorkplaceId,
        kind: String,
        position: Vec2,
        reason: &'static str,
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
    /// The building the pip is physically inside, which is not the same as who
    /// employs it: a hired pip is `employers = Some` and `inside = None` for the
    /// whole walk there, and stays that way if it arrives to a full building.
    pub inside: Vec<Option<WorkplaceId>>,
    /// Where the pip is going of its own accord, as opposed to where it is
    /// employed. An errand outranks the commute: a pip that cannot afford to
    /// keep working because it is starving has no business walking to work.
    ///
    /// This is the first thing in the simulation a pip decides for itself.
    /// Everything else about it — where it works, when it eats — was decided
    /// by a service or by the world.
    pub errands: Vec<Option<WorkplaceId>>,
    /// The core's replica of the pip's bank balance. Authoritative balances
    /// live in `services/bank`; this exists so a tick can decide "can this
    /// pip afford it" without a network call. Moved only by `Intent::Transfer`
    /// / `Intent::CreditBalances`.
    pub balances: Vec<i64>,

    // Workplaces, same structure-of-arrays treatment. There are few of them, so
    // the layout buys nothing here — it is uniformity, so that a reader who has
    // understood the pip arrays has understood these too.
    pub workplace_ids: Vec<WorkplaceId>,
    pub workplace_kinds: Vec<String>,
    pub workplace_positions: Vec<Vec2>,
    pub workplace_capacities: Vec<u32>,
    /// Cached count of `inside`. Maintained by `enter_workplaces`, `leave` and
    /// `remove_at`, and checked against a recount by a test — a derived value
    /// that drifts is worse than one recomputed every tick.
    pub workplace_occupants: Vec<u32>,
    /// Same replica role as `balances`, one per workplace.
    pub workplace_balances: Vec<i64>,
    /// What each workplace sells, and at what price. Registered from the
    /// service's own `Describe`, never invented here.
    pub workplace_sells: Vec<Vec<(ResourceKind, i64)>>,
    // Structures: services that stand in the world without employing anyone —
    // the bank, broadcast, the gateway itself. Same structure-of-arrays
    // treatment again, and deliberately separate from the workplace arrays: a
    // structure has no capacity, no occupancy and no payroll, so folding it in
    // would mean five arrays that are meaningless for half their entries.
    //
    // They do not affect the simulation. They are here rather than in the
    // renderer because position is world state and a pip can walk to a place in
    // the world — which is where this is heading. See ADR 0011.
    pub structure_ids: Vec<StructureId>,
    pub structure_kinds: Vec<String>,
    pub structure_positions: Vec<Vec2>,
    pub structure_roles: Vec<String>,
    /// Whether the service behind it answered its last health check. Observed
    /// by the gateway and recorded here, the same way capacity is.
    pub structure_healthy: Vec<bool>,

    /// The treasury's own balance. Negative by design: issuance is a transfer
    /// from the treasury, and money supply stays closed only because that
    /// negative and everyone else's positive balances sum to zero.
    pub treasury_balance: i64,

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
            inside: Vec::new(),
            errands: Vec::new(),
            balances: Vec::new(),
            workplace_ids: Vec::new(),
            workplace_kinds: Vec::new(),
            workplace_positions: Vec::new(),
            workplace_capacities: Vec::new(),
            workplace_occupants: Vec::new(),
            workplace_balances: Vec::new(),
            workplace_sells: Vec::new(),
            structure_ids: Vec::new(),
            structure_kinds: Vec::new(),
            structure_positions: Vec::new(),
            structure_roles: Vec::new(),
            structure_healthy: Vec::new(),
            treasury_balance: 0,
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

    pub fn workplace_index(&self, workplace: WorkplaceId) -> Option<usize> {
        self.workplace_ids.iter().position(|&id| id == workplace)
    }

    pub fn structure_index(&self, structure: StructureId) -> Option<usize> {
        self.structure_ids.iter().position(|&id| id == structure)
    }

    fn balance_of(&self, account: AccountId) -> Option<i64> {
        match account {
            AccountId::Pip(id) => self.index_of(id).map(|i| self.balances[i]),
            AccountId::Workplace(id) => {
                self.workplace_index(id).map(|i| self.workplace_balances[i])
            }
            AccountId::Treasury => Some(self.treasury_balance),
        }
    }

    fn adjust_balance(&mut self, account: AccountId, delta: i64) {
        match account {
            AccountId::Pip(id) => {
                if let Some(i) = self.index_of(id) {
                    self.balances[i] += delta;
                }
            }
            AccountId::Workplace(id) => {
                if let Some(i) = self.workplace_index(id) {
                    self.workplace_balances[i] += delta;
                }
            }
            AccountId::Treasury => self.treasury_balance += delta,
        }
    }

    /// Applies one unit's worth of `need_effects(kind)` to a pip, clamped
    /// exactly like `ApplyNeeds` — a resource is not trusted to keep a need in
    /// range any more than a workplace is.
    fn consume(&mut self, pip: PipId, kind: ResourceKind) {
        if let Some(i) = self.index_of(pip) {
            let effect = need_effects(kind);
            let n = &mut self.needs[i];
            n.food = (n.food + effect.food).clamp(0, NEED_MAX);
            n.rest = (n.rest + effect.rest).clamp(0, NEED_MAX);
            n.social = (n.social + effect.social).clamp(0, NEED_MAX);
        }
    }

    /// Takes the pip out of whatever building it is in. No-op if it is outside.
    fn leave_building(&mut self, i: usize) -> Option<WorkplaceId> {
        let workplace = self.inside[i].take()?;
        if let Some(wi) = self.workplace_index(workplace) {
            self.workplace_occupants[wi] = self.workplace_occupants[wi].saturating_sub(1);
        }
        Some(workplace)
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
        // Before wander and commute, because an errand outranks both: a
        // starving pip should not be strolling, and should not be walking to
        // a shift it will not live to finish.
        self.decide_errands();
        self.wander();
        self.commute();
        self.move_walkers();
        // After movement, so a pip that reached the door this tick gets in on
        // this tick rather than standing outside for one.
        self.enter_workplaces(&mut events);
        self.shop(&mut events);
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
            // Somebody with a job does not wander off. Without this, a pip
            // queuing at a full door would stroll away every few seconds and
            // start the walk over.
            if self.employers[i].is_some() {
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
                    self.inside.push(None);
                    self.errands.push(None);
                    self.balances.push(0);

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
                // Hiring is a contract, not a teleport. It records the employer
                // and nothing else; `commute` walks the pip there and
                // `enter_workplaces` decides whether it gets in.
                Intent::Hire { pip, workplace } => {
                    if let Some(i) = self.index_of(*pip) {
                        if self.employers[i] != Some(*workplace) {
                            self.leave_building(i);
                        }
                        self.employers[i] = Some(*workplace);
                    }
                }
                Intent::EndEmployment { pip } => {
                    if let Some(i) = self.index_of(*pip) {
                        if let Some(workplace) = self.leave_building(i) {
                            events.push(DomainEvent::PipEndedWork {
                                pip: *pip,
                                workplace,
                                reason: "employment ended",
                            });
                        }
                        self.employers[i] = None;
                        self.destinations[i] = None;
                        self.activities[i] = Activity::Idle;
                    }
                }
                Intent::RegisterWorkplace {
                    id,
                    kind,
                    position,
                    capacity,
                    sells,
                } => {
                    // Where a building stands is sim-core's fact, never the
                    // workplace's (ADR 0008) — so this is the one place that
                    // gets to say no. Off the map, or on top of another
                    // building, and the intent has no effect on the world —
                    // the gateway re-registers on a loop, so a rejected
                    // registration is retried for free. It is not, however,
                    // silent: unlike `Transfer` failing on an ordinary
                    // insufficient balance, this is almost always a
                    // configuration mistake that will not resolve itself, so
                    // it is worth a fact `server` can turn into a log line.
                    let in_bounds = (0..=WORLD_W_MILLI).contains(&position.x)
                        && (0..=WORLD_H_MILLI).contains(&position.y);
                    let min_spacing_sq =
                        (WORKPLACE_MIN_SPACING_MILLI as i64) * (WORKPLACE_MIN_SPACING_MILLI as i64);
                    let overlaps_another = self
                        .workplace_ids
                        .iter()
                        .zip(self.workplace_positions.iter())
                        .any(|(&other_id, &other_pos)| {
                            other_id != *id && position.distance_squared(other_pos) < min_spacing_sq
                        });
                    if !in_bounds || overlaps_another {
                        events.push(DomainEvent::WorkplaceRegistrationRejected {
                            workplace: *id,
                            kind: kind.clone(),
                            position: *position,
                            reason: if !in_bounds {
                                "off the map"
                            } else {
                                "overlaps another building"
                            },
                        });
                        continue;
                    }

                    // Upsert: the gateway re-registers on every reconnect, and
                    // a workplace that came back with a different capacity
                    // should be believed. Nobody is evicted if the new capacity
                    // is smaller — the overflow drains as shifts end.
                    if let Some(wi) = self.workplace_index(*id) {
                        self.workplace_kinds[wi] = kind.clone();
                        self.workplace_positions[wi] = *position;
                        self.workplace_capacities[wi] = *capacity;
                        self.workplace_sells[wi] = sells.clone();
                    } else {
                        self.workplace_ids.push(*id);
                        self.workplace_kinds.push(kind.clone());
                        self.workplace_positions.push(*position);
                        self.workplace_capacities.push(*capacity);
                        self.workplace_occupants.push(0);
                        self.workplace_balances.push(0);
                        self.workplace_sells.push(sells.clone());
                        events.push(DomainEvent::WorkplaceBuilt {
                            workplace: *id,
                            kind: kind.clone(),
                            position: *position,
                            capacity: *capacity,
                        });
                    }
                }
                Intent::RegisterStructure {
                    id,
                    kind,
                    position,
                    role,
                    healthy,
                } => {
                    // Upsert, like a workplace, but re-sent for a different
                    // reason: the gateway polls health, so `healthy` is the
                    // field that actually changes between two identical
                    // registrations.
                    //
                    // No domain event. A workplace being built changes the
                    // world; a service being deployed changes the picture of
                    // it, and putting that in the fact log would mean the
                    // replay of a world depended on which services happened to
                    // be up while it ran.
                    if let Some(si) = self.structure_index(*id) {
                        self.structure_kinds[si] = kind.clone();
                        self.structure_positions[si] = *position;
                        self.structure_roles[si] = role.clone();
                        self.structure_healthy[si] = *healthy;
                    } else {
                        self.structure_ids.push(*id);
                        self.structure_kinds.push(kind.clone());
                        self.structure_positions.push(*position);
                        self.structure_roles.push(role.clone());
                        self.structure_healthy.push(*healthy);
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
                Intent::Transfer {
                    payer,
                    payee,
                    amount,
                    resource_kind,
                    kind,
                } => {
                    // Rejected silently, no partial effect: the core is never
                    // behind on spending. It can only be behind on income not
                    // yet applied.
                    let Some(payer_balance) = self.balance_of(*payer) else {
                        continue;
                    };
                    if self.balance_of(*payee).is_none() {
                        continue;
                    }

                    // The treasury may go negative, because issuance is the
                    // money supply growing on purpose. That privilege is
                    // granted by the transfer's kind, never by the payer's
                    // identity — otherwise every movement out of the treasury
                    // could mint, and a mislabelled one would do it silently.
                    let may_run_negative =
                        *payer == AccountId::Treasury && *kind == TransferKind::Issuance;
                    if !may_run_negative && payer_balance < *amount {
                        continue;
                    }

                    // A purchase is a physical act. ADR 0004 made buildings
                    // places rather than addresses and hiring a walk rather
                    // than a teleport; buying is subject to the same rule, or
                    // money quietly reintroduces the addressing model that ADR
                    // replaced. The pip must be inside the seller.
                    if let Some(resource) = resource_kind {
                        let (AccountId::Pip(pip), AccountId::Workplace(seller)) = (*payer, *payee)
                        else {
                            continue;
                        };
                        let Some(i) = self.index_of(pip) else {
                            continue;
                        };
                        if self.inside[i] != Some(seller) {
                            continue;
                        }

                        self.adjust_balance(*payer, -*amount);
                        self.adjust_balance(*payee, *amount);
                        self.consume(pip, *resource);

                        // The core gates the purchase, so the core is what
                        // says it happened; the bank books this fact rather
                        // than deciding it. Emitted only on the path where
                        // the money actually moved.
                        events.push(DomainEvent::PurchaseMade {
                            pip,
                            workplace: seller,
                            amount: *amount,
                            resource: *resource,
                        });
                        continue;
                    }

                    self.adjust_balance(*payer, -*amount);
                    self.adjust_balance(*payee, *amount);
                }
                Intent::CreditBalances { payer, credits } => {
                    // Only pips that still exist are paid, and the payer is
                    // debited only for those. `adjust_balance` is a no-op for
                    // an unknown pip, so summing the whole list first would
                    // debit the workplace for a wage nobody receives — money
                    // destroyed, which the closed-supply invariant forbids.
                    // The gap is real: the gateway builds this list from a
                    // snapshot, and a pip can starve before the intent lands.
                    let payable: Vec<(PipId, i64)> = credits
                        .iter()
                        .filter(|(pip, _)| self.index_of(*pip).is_some())
                        .copied()
                        .collect();
                    let total: i64 = payable.iter().map(|(_, amount)| *amount).sum();
                    let Some(payer_balance) = self.balance_of(AccountId::Workplace(*payer)) else {
                        continue;
                    };
                    if payer_balance < total {
                        continue;
                    }
                    self.adjust_balance(AccountId::Workplace(*payer), -total);
                    for (pip, amount) in &payable {
                        self.adjust_balance(AccountId::Pip(*pip), *amount);
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

            // Company wears off. Slowly, and it never kills anyone — this is
            // not a second way to die, it is what gives the tavern a purpose.
            //
            // Added when the tavern went live and the measurement was
            // embarrassing: every pip sat at the maximum forever, because
            // nothing in the world consumed the need the tavern exists to
            // restore. Its only measurable effect was the food it cost.
            if self.tick.is_multiple_of(SOCIAL_DECAY_EVERY) {
                self.needs[i].social = (self.needs[i].social - 1).max(0);
            }

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
        // Before the arrays shift, or the freed position is lost and the
        // building slowly fills with the dead — the same class of bug the
        // farm's shift lease exists to catch on the other side of the wire.
        self.leave_building(i);

        // A dead pip's money goes back to the treasury rather than out of
        // existence. Money supply is closed (ADR 0006): the sum of every
        // balance is constant except at an explicit issuance, and dying is
        // routine here — starvation is the main way a pip leaves the world,
        // so leaking a purse per death would drain the supply steadily.
        //
        // Here rather than at the death site, so that any future way of
        // removing a pip inherits it. `services/bank` does the same on its
        // side, off the same PipDied fact.
        self.treasury_balance += self.balances[i];

        self.ids.remove(i);
        self.names.remove(i);
        self.positions.remove(i);
        self.destinations.remove(i);
        self.activities.remove(i);
        self.needs.remove(i);
        self.employers.remove(i);
        self.inside.remove(i);
        self.errands.remove(i);
        self.balances.remove(i);
    }

    /// Employed pips head for their building's door.
    ///
    /// Runs every tick rather than once at hire, so a pip knocked off course —
    /// or one whose workplace was registered after it was hired — finds its way
    /// there anyway. Idempotent: a pip already walking has its destination set
    /// to the same place.
    /// The cheapest place selling `kind` that this pip can both afford and
    /// reach, as an index into the workplace arrays.
    ///
    /// Ties break on price first, then on distance, then on the lower index.
    /// The last rule is arbitrary and exists only so that two runs of the same
    /// world make the same choice — reproducible beats clever.
    fn best_offer(&self, from: Vec2, budget: i64, kind: ResourceKind) -> Option<(usize, i64)> {
        let mut best: Option<(usize, i64, i64)> = None; // (index, price, distance)
        for wi in 0..self.workplace_ids.len() {
            let Some(&(_, price)) = self.workplace_sells[wi]
                .iter()
                .find(|(sold, _)| *sold == kind)
            else {
                continue;
            };
            if price > budget {
                continue;
            }
            let at = self.workplace_positions[wi];
            // Chebyshev, matching how pips actually move: `step_towards`
            // advances both axes at once, so the diagonal costs the same as a
            // straight line. Integer-only, because a float here would diverge
            // between the native and WASM builds.
            let distance = (at.x - from.x).abs().max((at.y - from.y).abs()) as i64;
            let better = match best {
                None => true,
                Some((_, best_price, best_distance)) => {
                    (price, distance) < (best_price, best_distance)
                }
            };
            if better {
                best = Some((wi, price, distance));
            }
        }
        best.map(|(wi, price, _)| (wi, price))
    }

    /// Hungry pips with money go and buy food.
    ///
    /// This is the first decision a pip makes for itself, and the rule lives
    /// here for the reason every other rule about pips does: the browser
    /// predicts with this same code, so a decision taken anywhere else would
    /// make the client and the server disagree about what happens next.
    ///
    /// Deliberately not a plan or a queue of goals. A pip is hungry or it is
    /// not, and it re-decides every tick — which means a shop that raises its
    /// price, or a wage that arrives late, changes the pip's mind without
    /// anything having to invalidate a stale intention.
    fn decide_errands(&mut self) {
        for i in 0..self.ids.len() {
            if self.needs[i].food > FOOD_HUNGRY_THRESHOLD {
                // Fed. Any errand it was on is finished, whether or not it
                // ever arrived.
                self.errands[i] = None;
                continue;
            }
            if self.errands[i].is_some() {
                continue;
            }

            let Some((wi, _)) =
                self.best_offer(self.positions[i], self.balances[i], ResourceKind::Food)
            else {
                // Nothing for sale it can afford. It keeps working, and if
                // that is not enough it starves — which is now an economic
                // death rather than a missing farm.
                continue;
            };

            self.errands[i] = Some(self.workplace_ids[wi]);
            // Leave work to go: a pip cannot be in two buildings, and its
            // place should go to someone who will use it.
            self.leave_building(i);
        }
    }

    /// A pip inside the shop it walked to buys one unit and leaves.
    ///
    /// Settled here rather than through an RPC because the decision is made
    /// against the pip's replica balance inside the tick — see ADR 0006. The
    /// bank books the fact afterwards; it does not get a vote.
    fn shop(&mut self, events: &mut Vec<DomainEvent>) {
        for i in 0..self.ids.len() {
            let Some(shop) = self.errands[i] else {
                continue;
            };
            if self.inside[i] != Some(shop) {
                continue;
            }
            let Some(wi) = self.workplace_index(shop) else {
                self.errands[i] = None;
                continue;
            };

            let offer = self.workplace_sells[wi]
                .iter()
                .find(|(sold, _)| *sold == ResourceKind::Food)
                .copied();

            // Whatever happens next, the errand is over: it either bought
            // something or found it could not, and standing in the doorway
            // re-deciding would hold a place it is not using.
            self.errands[i] = None;

            if let Some((kind, price)) = offer.filter(|(_, price)| self.balances[i] >= *price) {
                self.balances[i] -= price;
                self.workplace_balances[wi] += price;
                let pip = self.ids[i];
                self.consume(pip, kind);
                events.push(DomainEvent::PurchaseMade {
                    pip,
                    workplace: shop,
                    amount: price,
                    resource: kind,
                });
            }

            self.leave_building(i);
            self.activities[i] = Activity::Idle;
        }
    }

    fn commute(&mut self) {
        for i in 0..self.ids.len() {
            if self.inside[i].is_some() {
                continue;
            }
            // An errand outranks the commute. A pip walking to the shop is
            // not skiving: it is doing the thing that keeps it able to work
            // at all, and its employer holds the position while it goes.
            let (destination, activity) = match self.errands[i] {
                Some(shop) => (shop, Activity::Walking),
                None => match self.employers[i] {
                    Some(workplace) => (workplace, Activity::Commuting),
                    None => continue,
                },
            };
            let Some(wi) = self.workplace_index(destination) else {
                // Sent to a building the core has never been told about.
                // Nothing sensible to do but wait for it to be registered.
                continue;
            };

            let door = self.workplace_positions[wi];
            self.activities[i] = activity;
            self.destinations[i] = if self.positions[i] == door {
                None
            } else {
                Some(door)
            };
        }
    }

    /// Pips standing at a door go in, if there is room.
    ///
    /// This is where a building's capacity becomes physical. The number is the
    /// workplace service's own — the gateway registers it from `Describe` — so
    /// there is still exactly one owner of "how many fit"; the core only stops
    /// the twenty-fifth body walking through a door built for twenty-four.
    fn enter_workplaces(&mut self, events: &mut Vec<DomainEvent>) {
        for i in 0..self.ids.len() {
            if self.inside[i].is_some() {
                continue;
            }
            // A customer goes in for the same reason a worker does — the
            // building is a place, and buying is physical (ADR 0004, and
            // invariant 5 of ADR 0006). It takes a place while it is inside,
            // which is why `shop` puts it back out in the same tick.
            let shopping = self.errands[i].is_some();
            let Some(workplace) = self.errands[i].or(self.employers[i]) else {
                continue;
            };
            let Some(wi) = self.workplace_index(workplace) else {
                continue;
            };
            if self.positions[i] != self.workplace_positions[wi] {
                continue;
            }
            if self.workplace_occupants[wi] >= self.workplace_capacities[wi] {
                // Queues at the door, still Commuting, and tries again next
                // tick. Index order decides who gets the next free place, which
                // is arbitrary but reproducible — and reproducible is the
                // requirement.
                continue;
            }

            self.workplace_occupants[wi] += 1;
            self.inside[i] = Some(workplace);
            self.destinations[i] = None;

            if shopping {
                self.activities[i] = Activity::Eating;
                continue;
            }

            self.activities[i] = Activity::Working;
            events.push(DomainEvent::PipStartedWork {
                pip: self.ids[i],
                workplace,
            });
        }
    }

    fn move_walkers(&mut self) {
        for i in 0..self.positions.len() {
            let Some(dest) = self.destinations[i] else {
                continue;
            };

            self.positions[i] = self.positions[i].step_towards(dest, WALK_SPEED_MILLI);
            if self.positions[i] == dest {
                self.destinations[i] = None;
                // Only a wanderer goes idle on arrival. A commuter stays a
                // commuter until it is actually inside, which may be several
                // ticks later if the building is full.
                if self.activities[i] == Activity::Walking {
                    self.activities[i] = Activity::Idle;
                }
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
            mix(self.inside[i].unwrap_or(0));
            mix(self.balances[i] as u64);
            mix(self.errands[i].unwrap_or(0));
        }
        for i in 0..self.workplace_ids.len() {
            mix(self.workplace_ids[i]);
            mix(self.workplace_occupants[i] as u64);
            mix(self.workplace_balances[i] as u64);
        }
        // Structures are deliberately absent, and this is not an oversight.
        //
        // The hash answers "did two runs of the simulation diverge". A
        // structure is not simulation: `healthy` flips because a container was
        // restarted, and the client's WASM core registers no structures at all.
        // Mixing either in would make the browser look divergent from the
        // server on a world that matches perfectly, and `make parity` would
        // fail for a reason that has nothing to do with the core.
        mix(self.treasury_balance as u64);
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

    /// The tavern is only worth walking to if company runs out.
    ///
    /// This pins a real measurement rather than a hypothetical: with the tavern
    /// deployed and nothing consuming social, every pip sat at 1000 forever and
    /// the workplace's whole purpose was decorative.
    #[test]
    fn company_wears_off_but_never_kills() {
        let mut w = World::new(31);
        w.step(&spawn(1));
        let before = w.needs[0].social;

        for _ in 0..400 {
            w.step(&[]);
        }

        assert!(
            w.needs[0].social < before,
            "social never decayed, so nothing in the world needs a tavern"
        );
        assert_eq!(w.len(), 1, "social must not be a second way to die");

        // And a tavern shift puts it back.
        let drained = w.needs[0].social;
        w.step(&[Intent::ApplyNeeds {
            pip: 1,
            food: 0,
            rest: 0,
            social: 200,
        }]);
        assert!(w.needs[0].social > drained);
    }

    // --- buildings ----------------------------------------------------------

    const DOOR: Vec2 = Vec2 { x: 6_000, y: 4_000 };

    fn build(id: WorkplaceId, capacity: u32) -> Intent {
        Intent::RegisterWorkplace {
            id,
            kind: "farm".into(),
            position: DOOR,
            capacity,
            sells: Vec::new(),
        }
    }

    /// Recount `inside` and compare against the cached occupancy. The cache is
    /// maintained in three places, and a derived value that drifts is worse
    /// than one recomputed every tick — so every building test ends here.
    fn assert_occupancy_is_consistent(w: &World) {
        for wi in 0..w.workplace_ids.len() {
            let counted = w
                .inside
                .iter()
                .filter(|p| **p == Some(w.workplace_ids[wi]))
                .count() as u32;
            assert_eq!(
                counted, w.workplace_occupants[wi],
                "workplace {} cached {} occupants but holds {}",
                w.workplace_ids[wi], w.workplace_occupants[wi], counted
            );
            assert!(
                w.workplace_occupants[wi] <= w.workplace_capacities[wi],
                "workplace {} is over capacity",
                w.workplace_ids[wi]
            );
        }
    }

    /// Hiring is a contract, not a teleport.
    #[test]
    fn a_hired_pip_walks_to_work_before_it_starts_working() {
        let mut w = World::new(9);
        w.step(&spawn(1));
        let events = w.step(&[
            build(77, 4),
            Intent::Hire {
                pip: 1,
                workplace: 77,
            },
        ]);

        assert_eq!(w.employers[0], Some(77));
        assert_eq!(w.inside[0], None, "hired and already inside");
        assert_eq!(w.activities[0], Activity::Commuting);
        assert!(
            !events
                .iter()
                .any(|e| matches!(e, DomainEvent::PipStartedWork { .. })),
            "work cannot have started before the pip arrived"
        );

        let mut started = None;
        for _ in 0..200 {
            for e in w.step(&[]) {
                if let DomainEvent::PipStartedWork { workplace, .. } = e {
                    started = Some(workplace);
                }
            }
            if started.is_some() {
                break;
            }
        }

        assert_eq!(started, Some(77), "the pip never got to work");
        assert_eq!(w.positions[0], DOOR);
        assert_eq!(w.inside[0], Some(77));
        assert_eq!(w.activities[0], Activity::Working);
        assert_occupancy_is_consistent(&w);
    }

    /// The limit the whole feature exists for.
    #[test]
    fn a_building_never_holds_more_pips_than_it_fits() {
        const CAPACITY: u32 = 3;

        let mut w = World::new(21);
        w.step(&spawn(10));
        let hires: Vec<Intent> = (1..=10)
            .map(|pip| Intent::Hire { pip, workplace: 5 })
            .collect();
        w.step(&[build(5, CAPACITY)]);
        w.step(&hires);

        for _ in 0..400 {
            w.step(&[]);
            assert_occupancy_is_consistent(&w);
        }

        assert_eq!(w.workplace_occupants[0], CAPACITY);

        // The rest are not lost or wandering — they are queuing at the door,
        // which is the behaviour that makes the limit visible on screen.
        let queuing = (0..w.len())
            .filter(|&i| w.inside[i].is_none() && w.activities[i] == Activity::Commuting)
            .count();
        assert_eq!(queuing, 10 - CAPACITY as usize);
        for i in 0..w.len() {
            if w.inside[i].is_none() {
                assert_eq!(w.positions[i], DOOR, "a queuing pip should be at the door");
            }
        }
    }

    #[test]
    fn a_freed_place_is_taken_by_someone_waiting() {
        let mut w = World::new(22);
        w.step(&spawn(2));
        w.step(&[build(5, 1)]);
        w.step(&[
            Intent::Hire {
                pip: 1,
                workplace: 5,
            },
            Intent::Hire {
                pip: 2,
                workplace: 5,
            },
        ]);

        for _ in 0..200 {
            w.step(&[]);
        }
        assert_eq!(w.workplace_occupants[0], 1);
        let inside_first = (0..w.len()).find(|&i| w.inside[i].is_some()).unwrap();
        let first = w.ids[inside_first];

        w.step(&[Intent::EndEmployment { pip: first }]);
        w.step(&[]);

        assert_eq!(w.workplace_occupants[0], 1, "the waiting pip should be in");
        let now_inside = (0..w.len()).find(|&i| w.inside[i].is_some()).unwrap();
        assert_ne!(w.ids[now_inside], first);
        assert_occupancy_is_consistent(&w);
    }

    /// Dying at work must free the place. The equivalent bug on the farm's side
    /// of the wire is what the shift lease exists to catch.
    #[test]
    fn dying_inside_frees_the_place() {
        let mut w = World::new(23);
        w.step(&spawn(1));
        w.step(&[build(5, 1)]);
        w.step(&[Intent::Hire {
            pip: 1,
            workplace: 5,
        }]);

        for _ in 0..200 {
            w.step(&[]);
        }
        assert_eq!(w.workplace_occupants[0], 1);

        w.step(&[Intent::ApplyNeeds {
            pip: 1,
            food: -999_999,
            rest: 0,
            social: 0,
        }]);

        assert!(w.is_empty());
        assert_eq!(w.workplace_occupants[0], 0);
        assert_occupancy_is_consistent(&w);
    }

    /// Buildings and queues must not cost the property everything rests on.
    #[test]
    fn commuting_and_queuing_are_reproducible() {
        let run = || {
            let mut w = World::new(77);
            w.step(&spawn(30));
            w.step(&[build(5, 8)]);
            for t in 0..600 {
                let intents = if t % 20 == 0 && t / 20 < 30 {
                    vec![Intent::Hire {
                        pip: (t / 20 + 1) as PipId,
                        workplace: 5,
                    }]
                } else {
                    vec![]
                };
                w.step(&intents);
            }
            w
        };
        assert_eq!(run().state_hash(), run().state_hash());
    }

    #[test]
    fn registering_a_workplace_twice_updates_it_without_a_second_building() {
        let mut w = World::new(24);
        let first = w.step(&[build(5, 4)]);
        assert_eq!(
            first
                .iter()
                .filter(|e| matches!(e, DomainEvent::WorkplaceBuilt { .. }))
                .count(),
            1
        );

        let again = w.step(&[Intent::RegisterWorkplace {
            id: 5,
            kind: "farm".into(),
            position: Vec2 { x: 1, y: 2 },
            capacity: 9,
            sells: Vec::new(),
        }]);
        assert!(!again
            .iter()
            .any(|e| matches!(e, DomainEvent::WorkplaceBuilt { .. })));
        assert_eq!(w.workplace_ids.len(), 1);
        assert_eq!(w.workplace_capacities[0], 9);
        assert_eq!(w.workplace_positions[0], Vec2 { x: 1, y: 2 });
    }

    #[test]
    fn a_workplace_cannot_register_on_top_of_another() {
        let mut w = World::new(24);
        w.step(&[build(5, 24)]);

        // Same door, different id: the gateway never asks for this, but
        // nothing stops it either, and sim-core is the only place left to say
        // no — see ADR 0008.
        let rejected = w.step(&[Intent::RegisterWorkplace {
            id: 7,
            kind: "tavern".into(),
            position: DOOR,
            capacity: 24,
            sells: Vec::new(),
        }]);

        assert!(!rejected
            .iter()
            .any(|e| matches!(e, DomainEvent::WorkplaceBuilt { .. })));
        assert_eq!(w.workplace_ids.len(), 1, "the second building never lands");
        assert!(rejected.iter().any(|e| matches!(
            e,
            DomainEvent::WorkplaceRegistrationRejected {
                workplace: 7,
                reason: "overlaps another building",
                ..
            }
        )));
    }

    #[test]
    fn a_workplace_cannot_register_off_the_map() {
        let mut w = World::new(24);
        let rejected = w.step(&[Intent::RegisterWorkplace {
            id: 5,
            kind: "farm".into(),
            position: Vec2 {
                x: WORLD_W_MILLI + 1,
                y: 0,
            },
            capacity: 24,
            sells: Vec::new(),
        }]);

        assert!(!rejected
            .iter()
            .any(|e| matches!(e, DomainEvent::WorkplaceBuilt { .. })));
        assert!(w.workplace_ids.is_empty());
        assert!(rejected.iter().any(|e| matches!(
            e,
            DomainEvent::WorkplaceRegistrationRejected {
                workplace: 5,
                reason: "off the map",
                ..
            }
        )));
    }

    #[test]
    fn moving_a_registered_workplace_onto_another_is_also_rejected() {
        let mut w = World::new(24);
        w.step(&[
            build(5, 24),
            Intent::RegisterWorkplace {
                id: 7,
                kind: "tavern".into(),
                position: Vec2 {
                    x: DOOR.x + WORKPLACE_MIN_SPACING_MILLI * 4,
                    y: DOOR.y,
                },
                capacity: 24,
                sells: Vec::new(),
            },
        ]);

        // 7 tries to update itself onto 5's door. Not just insertion is
        // guarded — an update that would collide is refused the same way, so
        // the position on file for 7 is left exactly where it was.
        let rejected = w.step(&[Intent::RegisterWorkplace {
            id: 7,
            kind: "tavern".into(),
            position: DOOR,
            capacity: 24,
            sells: Vec::new(),
        }]);

        let wi = w.workplace_index(7).unwrap();
        assert_ne!(w.workplace_positions[wi], DOOR);
        assert!(rejected.iter().any(|e| matches!(
            e,
            DomainEvent::WorkplaceRegistrationRejected {
                workplace: 7,
                reason: "overlaps another building",
                ..
            }
        )));
    }

    // --- structures -----------------------------------------------------

    fn deploy(id: StructureId, healthy: bool) -> Intent {
        Intent::RegisterStructure {
            id,
            kind: "bank".into(),
            position: Vec2 { x: 3_000, y: 4_000 },
            role: "double-entry ledger".into(),
            healthy,
        }
    }

    /// Health is the field that changes, so re-registering has to update it in
    /// place — the gateway re-sends this on every poll.
    #[test]
    fn registering_a_structure_twice_updates_health_without_a_second_building() {
        let mut w = World::new(31);
        w.step(&[deploy(1, true)]);
        assert_eq!(w.structure_ids.len(), 1);
        assert!(w.structure_healthy[0]);

        w.step(&[deploy(1, false)]);
        assert_eq!(w.structure_ids.len(), 1, "a re-register built a second one");
        assert!(!w.structure_healthy[0], "health did not follow the intent");
    }

    /// A structure is scenery. If it ever starts moving pips or money, it has
    /// stopped being one, and this test is the tripwire.
    #[test]
    fn a_structure_does_not_touch_the_simulation() {
        let mut with = World::new(77);
        with.step(&spawn(20));
        let mut without = World::new(77);
        without.step(&spawn(20));

        for t in 0..300 {
            let extra = if t == 10 {
                vec![deploy(1, true)]
            } else {
                vec![]
            };
            with.step(&extra);
            without.step(&[]);
        }

        assert_eq!(
            with.state_hash(),
            without.state_hash(),
            "registering a structure changed the simulation"
        );
        assert_eq!(with.structure_ids.len(), 1);
        assert_eq!(without.structure_ids.len(), 0);
    }

    // --- money ----------------------------------------------------------

    #[test]
    fn ale_restores_social_and_grain_does_nothing() {
        assert_eq!(need_effects(ResourceKind::Ale).social, 150);
        assert_eq!(
            need_effects(ResourceKind::Grain),
            Needs {
                food: 0,
                rest: 0,
                social: 0,
            }
        );
    }

    /// A workplace is paid, and the pip's replica balance moves. This is the
    /// payroll path: one batch intent, not one Transfer per pip.
    #[test]
    fn credit_balances_pays_every_pip_in_one_intent() {
        let mut w = World::new(40);
        w.step(&spawn(2));
        w.step(&[build(9, 8)]);
        // Fund the workplace first — a payer with nothing to pay cannot pay.
        w.step(&[Intent::Transfer {
            payer: AccountId::Treasury,
            payee: AccountId::Workplace(9),
            amount: 100,
            resource_kind: None,
            kind: TransferKind::Issuance,
        }]);

        w.step(&[Intent::CreditBalances {
            payer: 9,
            credits: vec![(1, 30), (2, 20)],
        }]);

        assert_eq!(w.balances[0], 30);
        assert_eq!(w.balances[1], 20);
        assert_eq!(w.workplace_balances[0], 50);
    }

    /// A payer that cannot cover the sum pays nobody — no partial payroll.
    #[test]
    fn credit_balances_rejects_as_a_whole_if_underfunded() {
        let mut w = World::new(41);
        w.step(&spawn(2));
        w.step(&[build(9, 8)]);

        w.step(&[Intent::CreditBalances {
            payer: 9,
            credits: vec![(1, 30), (2, 20)],
        }]);

        assert_eq!(w.balances[0], 0);
        assert_eq!(w.balances[1], 0);
        assert_eq!(w.workplace_balances[0], 0);
    }

    /// Walk a hired pip until it is inside its workplace. Buying is physical,
    /// so most money tests need a pip that has actually arrived somewhere.
    fn walk_inside(w: &mut World, pip: PipId, workplace: WorkplaceId) {
        w.step(&[Intent::Hire { pip, workplace }]);
        for _ in 0..400 {
            let i = w.index_of(pip).expect("pip should still exist");
            if w.inside[i] == Some(workplace) {
                return;
            }
            w.step(&[]);
        }
        panic!("pip never reached its workplace");
    }

    /// Buying a resource moves money and consumes it in the same intent.
    #[test]
    fn transfer_with_a_resource_kind_pays_and_consumes() {
        let mut w = World::new(42);
        w.step(&spawn(1));
        w.step(&[build(9, 8)]);
        w.step(&[Intent::Transfer {
            payer: AccountId::Treasury,
            payee: AccountId::Pip(1),
            amount: 10,
            resource_kind: None,
            kind: TransferKind::Issuance,
        }]);
        walk_inside(&mut w, 1, 9);

        let social_before = w.needs[0].social;
        let events = w.step(&[Intent::Transfer {
            payer: AccountId::Pip(1),
            payee: AccountId::Workplace(9),
            amount: 4,
            resource_kind: Some(ResourceKind::Ale),
            kind: TransferKind::Purchase,
        }]);

        assert_eq!(w.balances[0], 6);
        assert_eq!(w.workplace_balances[0], 4);
        assert!(w.needs[0].social >= social_before);
        assert!(
            events
                .iter()
                .any(|e| matches!(e, DomainEvent::PurchaseMade { .. })),
            "the core gates the purchase, so it is what reports one"
        );
    }

    /// ADR 0004 made buildings places rather than addresses. Buying is subject
    /// to the same rule: a pip cannot shop from across the map, or money
    /// quietly reintroduces the model that ADR replaced.
    #[test]
    fn a_pip_cannot_buy_from_a_building_it_is_not_inside() {
        let mut w = World::new(42);
        w.step(&spawn(1));
        w.step(&[build(9, 8)]);
        w.step(&[Intent::Transfer {
            payer: AccountId::Treasury,
            payee: AccountId::Pip(1),
            amount: 10,
            resource_kind: None,
            kind: TransferKind::Issuance,
        }]);
        assert_eq!(w.inside[0], None, "the pip is outside to begin with");

        let social_before = w.needs[0].social;
        let events = w.step(&[Intent::Transfer {
            payer: AccountId::Pip(1),
            payee: AccountId::Workplace(9),
            amount: 4,
            resource_kind: Some(ResourceKind::Ale),
            kind: TransferKind::Purchase,
        }]);

        assert_eq!(w.balances[0], 10, "no money moved");
        assert_eq!(w.workplace_balances[0], 0);
        assert!(w.needs[0].social <= social_before, "nothing was consumed");
        assert!(
            !events
                .iter()
                .any(|e| matches!(e, DomainEvent::PurchaseMade { .. })),
            "a rejected purchase is not a fact"
        );
    }

    /// The treasury may go negative, but only for an issuance. The privilege
    /// is carried by the transfer, never inferred from who is paying —
    /// otherwise any movement out of the treasury could mint.
    #[test]
    fn only_an_issuance_lets_the_treasury_run_negative() {
        let mut w = World::new(42);
        w.step(&spawn(1));

        w.step(&[Intent::Transfer {
            payer: AccountId::Treasury,
            payee: AccountId::Pip(1),
            amount: 100,
            resource_kind: None,
            kind: TransferKind::Wage,
        }]);
        assert_eq!(w.balances[0], 0, "a treasury with nothing cannot pay wages");
        assert_eq!(w.treasury_balance, 0);

        w.step(&[Intent::Transfer {
            payer: AccountId::Treasury,
            payee: AccountId::Pip(1),
            amount: 100,
            resource_kind: None,
            kind: TransferKind::Issuance,
        }]);
        assert_eq!(w.balances[0], 100, "an issuance mints");
        assert_eq!(w.treasury_balance, -100);
        assert_eq!(total_money(&w), 0, "and the supply stays closed");
    }

    /// A pip can never spend money it does not have — the transfer is
    /// rejected outright, not clamped to what's available.
    #[test]
    fn transfer_rejects_insufficient_balance() {
        let mut w = World::new(43);
        w.step(&spawn(1));
        w.step(&[build(9, 8)]);

        w.step(&[Intent::Transfer {
            payer: AccountId::Pip(1),
            payee: AccountId::Workplace(9),
            amount: 4,
            resource_kind: Some(ResourceKind::Ale),
            kind: TransferKind::Purchase,
        }]);

        assert_eq!(w.balances[0], 0);
        assert_eq!(w.workplace_balances[0], 0);
    }

    /// Balances are mixed into state_hash, so an economy that diverges is
    /// caught by replay exactly like a position or a need would be.
    #[test]
    fn state_hash_changes_with_balances() {
        let mut w = World::new(44);
        w.step(&spawn(1));
        let before = w.state_hash();

        w.step(&[Intent::Transfer {
            payer: AccountId::Treasury,
            payee: AccountId::Pip(1),
            amount: 10,
            resource_kind: None,
            kind: TransferKind::Issuance,
        }]);

        assert_ne!(before, w.state_hash());
    }

    /// Every balance the core replicates, summed. Issuance is the treasury
    /// paying out, so its balance goes negative by exactly what everyone else
    /// holds — a closed supply sums to zero, always.
    fn total_money(w: &World) -> i64 {
        w.balances.iter().sum::<i64>()
            + w.workplace_balances.iter().sum::<i64>()
            + w.treasury_balance
    }

    /// The invariant the whole ledger design rests on, stated as a test.
    ///
    /// It failed before the escheat in `remove_at`: a pip that starved took
    /// its balance out of existence, and since starvation is the ordinary way
    /// to die here, the supply drained a purse at a time.
    #[test]
    fn a_dying_pip_returns_its_money_to_the_treasury() {
        let mut w = World::new(40);
        w.step(&spawn(1));
        w.step(&[Intent::Transfer {
            payer: AccountId::Treasury,
            payee: AccountId::Pip(1),
            amount: 500,
            resource_kind: None,
            kind: TransferKind::Issuance,
        }]);
        assert_eq!(w.balances[0], 500);
        assert_eq!(total_money(&w), 0, "issuance keeps the supply closed");

        // Starve it. Food drains every tick and nothing feeds it.
        for _ in 0..2_000 {
            if w.is_empty() {
                break;
            }
            w.step(&[]);
        }

        assert!(w.is_empty(), "the pip should have starved");
        assert_eq!(w.treasury_balance, 0, "the purse came back");
        assert_eq!(total_money(&w), 0, "money did not vanish with the pip");
    }

    /// The gateway builds payroll from a snapshot, so a pip can die between
    /// being listed and the intent being applied. Paying it must not debit the
    /// workplace for a wage that lands nowhere.
    #[test]
    fn payroll_skips_a_pip_that_no_longer_exists() {
        let mut w = World::new(40);
        w.step(&spawn(1));
        w.step(&[build(9, 4)]);
        w.step(&[Intent::Transfer {
            payer: AccountId::Treasury,
            payee: AccountId::Workplace(9),
            amount: 1_000,
            resource_kind: None,
            kind: TransferKind::Issuance,
        }]);
        let before = total_money(&w);

        w.step(&[Intent::CreditBalances {
            payer: 9,
            credits: vec![(1, 10), (99_999, 10)],
        }]);

        assert_eq!(w.balances[0], 10, "the living pip is paid");
        assert_eq!(
            w.workplace_balances[0], 990,
            "the workplace is debited only for the wage that was actually paid"
        );
        assert_eq!(total_money(&w), before, "no money created or destroyed");
    }

    /// A shop: a workplace nobody works at, which sells food at `price`.
    fn shop_at(id: WorkplaceId, position: Vec2, price: i64) -> Intent {
        Intent::RegisterWorkplace {
            id,
            kind: "market".into(),
            position,
            capacity: 8,
            sells: vec![(ResourceKind::Food, price)],
        }
    }

    /// The whole point of ADR 0006, as one test: work pays money, money buys
    /// food, food keeps a pip alive. Before this, food was conjured by the
    /// workplace and the economy was decorative.
    ///
    /// The pip is paid the way the gateway pays it — a batch credit once a
    /// second of simulated time — and has to decide for itself when to stop
    /// working and go and eat.
    #[test]
    fn a_paid_pip_feeds_itself_and_survives() {
        let mut w = World::new(7);
        w.step(&spawn(1));
        w.step(&[
            build(9, 4),
            shop_at(
                10,
                Vec2 {
                    x: 20_000,
                    y: 4_000,
                },
                50,
            ),
            Intent::Transfer {
                payer: AccountId::Treasury,
                payee: AccountId::Workplace(9),
                amount: 1_000_000,
                resource_kind: None,
                kind: TransferKind::Issuance,
            },
            Intent::Hire {
                pip: 1,
                workplace: 9,
            },
        ]);

        let mut bought = 0;
        for tick in 0..4_000 {
            assert!(!w.is_empty(), "the pip starved at tick {tick}");

            // Payroll, as the gateway runs it: once every ten ticks, only for
            // a pip that is actually inside its workplace.
            let intents = if tick % 10 == 0 && w.inside[0] == Some(9) {
                vec![Intent::CreditBalances {
                    payer: 9,
                    credits: vec![(1, 60)],
                }]
            } else {
                Vec::new()
            };

            bought += w
                .step(&intents)
                .iter()
                .filter(|e| matches!(e, DomainEvent::PurchaseMade { .. }))
                .count();
        }

        assert!(bought > 5, "expected repeat custom, got {bought} purchases");
        assert!(
            w.balances[0] > 0,
            "wages should outrun the grocery bill, not merely match it"
        );
        assert_eq!(total_money(&w), 0, "the supply stayed closed throughout");
    }

    /// The other side of the same coin: money is what stands between a pip and
    /// starvation, so a pip with none dies even though food exists and it is
    /// standing next to it. This is the scarcity the ADR was after — dying of
    /// poverty rather than of a missing building.
    #[test]
    fn a_penniless_pip_starves_next_to_food_it_cannot_afford() {
        let mut w = World::new(7);
        w.step(&spawn(1));
        w.step(&[shop_at(10, DOOR, 50)]);

        for _ in 0..2_000 {
            if w.is_empty() {
                break;
            }
            w.step(&[]);
        }

        assert!(w.is_empty(), "a pip with no money should not survive");
    }

    /// Hunger outranks the commute. A pip that would otherwise walk to work
    /// goes to the shop instead, and its employer keeps the position.
    #[test]
    fn an_errand_outranks_the_commute() {
        let mut w = World::new(7);
        w.step(&spawn(1));
        w.step(&[
            build(9, 4),
            shop_at(
                10,
                Vec2 {
                    x: 30_000,
                    y: 20_000,
                },
                10,
            ),
            Intent::Transfer {
                payer: AccountId::Treasury,
                payee: AccountId::Pip(1),
                amount: 100,
                resource_kind: None,
                kind: TransferKind::Issuance,
            },
            Intent::Hire {
                pip: 1,
                workplace: 9,
            },
        ]);

        // Starve it to just under the threshold while it commutes.
        while w.needs[0].food > FOOD_HUNGRY_THRESHOLD {
            w.step(&[]);
        }
        w.step(&[]);

        assert_eq!(w.errands[0], Some(10), "hunger sent it shopping");
        assert_eq!(w.employers[0], Some(9), "it is still employed");
        assert_eq!(w.activities[0], Activity::Walking);
    }

    /// Payroll is all-or-nothing against what is payable, and the check runs
    /// after the dead are filtered out — a workplace that can afford its live
    /// workers is not blocked by names on the list that no longer exist.
    #[test]
    fn payroll_affordability_is_checked_after_filtering_the_dead() {
        let mut w = World::new(40);
        w.step(&spawn(1));
        w.step(&[build(9, 4)]);
        w.step(&[Intent::Transfer {
            payer: AccountId::Treasury,
            payee: AccountId::Workplace(9),
            amount: 10,
            resource_kind: None,
            kind: TransferKind::Issuance,
        }]);

        // 10 covers the living pip alone; it would not cover both entries.
        w.step(&[Intent::CreditBalances {
            payer: 9,
            credits: vec![(1, 10), (99_999, 10)],
        }]);

        assert_eq!(w.balances[0], 10, "the living pip is paid");
        assert_eq!(w.workplace_balances[0], 0);
        assert_eq!(total_money(&w), 0);
    }
}
