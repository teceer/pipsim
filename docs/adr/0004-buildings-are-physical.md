# 4. Buildings are physical, and capacity has one owner

Status: accepted

## Context

Until now a workplace was an address, not a place. `Hire` set a pip's activity
to `Working` on the spot: a pip on the far side of the map became a farmhand
without moving, and the farm's `max_workers = 24` existed only as a number
inside a Go process. Nothing on screen showed a building, and nothing stopped
five hundred pips "working" in a shed.

Making buildings real raises a rule-4 problem immediately. "How many pips fit"
is now enforced in two places — the farm decides who it employs, the core
decides who fits through the door — and rule 4 says no domain rule is
implemented in two languages.

## Decision

**One number, two enforcements.** `max_workers` is owned by the workplace
service and nowhere else. The gateway reads it from `Describe` and copies it
into the core with a `RegisterWorkplace` intent. The core never invents a
capacity and never adjusts one.

What each side then enforces is genuinely different:

| | Owner | Enforces |
|---|---|---|
| Employment capacity | the workplace service | who is on the payroll |
| Physical occupancy | sim-core | how many bodies are in the room |

The rule is duplicated only if you read it as one rule. It is not: a pip can be
employed and not present, and that state lasts as long as the walk.

**Hiring is a contract, not a teleport.** `Hire` records an employer. The core
then walks the pip to the building every tick (`commute`), and lets it in on
arrival if there is room (`enter_workplaces`). A pip that arrives at a full
building queues at the door — employed, outside, `Activity::Commuting` — and
takes the next place that frees up, in index order, which is arbitrary but
reproducible.

`employers[i]` and `inside[i]` are therefore separate arrays, and the wire
format carries both.

## Consequences

- **`PipStartedWork` now means what it says.** It is emitted on entering the
  building, not on being hired. The old behaviour put a fact in Kafka that
  misreported where the pip was for the next thirty seconds.
- **The gateway keeps calling `Work` for a commuting pip, but withholds the
  effects.** A commute across the map takes longer than the farm's fifteen
  second shift lease, so skipping the call would have the farm reap a shift the
  pip is walking to. Because the farm prices by elapsed ticks rather than per
  call, the walk is not paid retroactively either.
- **Walking speed went from 50 to 150 milli-tiles per tick.** At the old speed
  the worst-case commute was 72 seconds against a food drain of one per tick;
  employment would have killed more pips than it fed. This is the first time a
  number in the core was set by an economic constraint rather than by feel, and
  it is worth noticing that the constraint only became visible once the walk
  existed.
- **Registration is a loop, not a startup step.** sim-core restarts with a fresh
  world and forgets every building. The gateway re-registers every ten seconds;
  the intent is idempotent, so that costs nothing and removes an ordering
  dependency between two deployments.
- **Occupancy is a cached count.** `workplace_occupants` is derived from
  `inside`, maintained in three places, and checked against a full recount by a
  test that every building test calls. A derived value that drifts is worse than
  one recomputed every tick, and this is the cheap way to keep both.
- The client hides pips that are inside. Drawing them on the roof would make the
  limit meaningless to look at; the building's front face carries an occupancy
  band instead.
