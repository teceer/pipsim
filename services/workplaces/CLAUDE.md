# workplaces

Every workplace implements exactly one contract: `pips.workplace.v1.WorkplaceService`.

This directory is where the polyglot goal is exercised on purpose. Workplaces are
small, isolated and identically shaped, so rotating languages costs little and
blowing one up does not take the game down.

| Workplace | Language | Produces |
|---|---|---|
| `farm` | Go | grain |
| `workshop` | TypeScript | tools |
| `tavern` | Elixir | ale, restores the social need |

## The rule that keeps this working

**If you want to extend `workplace.proto` for one specific building, stop.**
That is the signal the abstraction is wrong, and the fix is almost always to
express the building's specialness through `Describe` metadata or through the
resources it consumes and produces — not through a new RPC that only one
implementation answers.

The payoff of holding that line: adding a new building type is an hour of work
in any language and touches neither sim-core nor the gateway.

## What every workplace must do

- implement all five RPCs (`Describe`, `CanEmploy`, `StartShift`, `Work`, `EndShift`)
- own its own Postgres schema; never read another service's tables
- publish `ResourceProduced` facts through the gateway, not directly
- expose `/healthz`, JSON logs, and OTel spans named `pipsim.<kind>.<operation>`
- provide the standard Make targets: `run` `test` `lint` `build` `gen`

## What no workplace may do

- know that another workplace exists
- hold pip state — pips belong to sim-core; a workplace knows only who is on
  shift right now
- decide anything about needs beyond returning `need_deltas` from `Work`

## What the farm established

It is the template, and two of its choices are worth copying:

**Shifts are held on a lease.** A shift nobody asks to `Work` for fifteen
seconds expires by itself. This is not defensive coding — it fixes a failure
seen in the cluster, where sim-core restarted with a fresh world and the farm
went on holding every position for pips that no longer existed, so nobody could
be hired again. A lease beats a reconciliation RPC because a workplace should
not have to ask anyone who still exists.

**`Work` pays for elapsed ticks, not per call.** The driver batches — it calls
`Work` once a second while the world ticks ten times. Flat per-call amounts made
employment a *slower death* than idling, because a working pip drains food every
tick and was paid once. Scaling by `tick - lastWorkTick` makes the contract
independent of whatever cadence the caller picks, and the amount is capped so a
stalled driver cannot hand out a windfall.

## Adding a new one

1. Copy the closest existing workplace.
2. Implement the five RPCs.
3. Add it to `services` in the root `Makefile` and to the loop in `Tiltfile`.
4. Add a Helm chart under `infra/helm/`.

No change to `proto/`, sim-core or the gateway should be needed. If one is, the
contract is leaking.
