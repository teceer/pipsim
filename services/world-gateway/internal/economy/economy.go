// Package economy drives the loop between sim-core and the workplaces.
//
// The split it is careful about: this package contains *mechanics*, not policy.
// It fills empty positions and forwards the consequences of a shift; it does
// not decide who deserves work, what a shift costs, or whether a pip survives.
// The workplaces own the first, sim-core owns the rest.
//
// Concretely, it never looks at a pip's needs. It offers whoever is unemployed
// to the work exchange and lets a workplace with capacity claim them, so
// scarcity emerges from the workplaces rather than from a rule written here.
// If this file ever grows an `if needs[food] < …`, the boundary has leaked.
package economy

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/teceer/pipsim/gen/go/pips/bank/v1/bankv1connect"
	simv1 "github.com/teceer/pipsim/gen/go/pips/sim/v1"
	"github.com/teceer/pipsim/gen/go/pips/sim/v1/simv1connect"
	workplacev1 "github.com/teceer/pipsim/gen/go/pips/workplace/v1"
)

// Fleet runs every workplace the gateway knows about off one heartbeat.
//
// One heartbeat and one snapshot, rather than a loop per workplace. Two drivers
// polling independently would see two different worlds a fraction of a second
// apart and disagree about who is employed — and the whole point of deriving
// employment from sim-core is that there is one answer.
type Fleet struct {
	sim     simv1connect.SimServiceClient
	bank    bankv1connect.BankServiceClient
	drivers []*Driver

	// Where the next round starts handing out offers.
	//
	// Without it the first workplace in the list would be offered every
	// unemployed pip first, every round, and the last would only ever see the
	// leftovers. Rotating is not fairness for its own sake: it is what stops
	// the order of an environment variable from deciding the economy.
	mu   sync.Mutex
	next int
}

// NewFleet wires the sim-core client every driver shares. `bank` may be nil —
// a gateway with no bank reachable still runs the economy, it just never
// pays wages or lets a pip buy anything.
func NewFleet(sim simv1connect.SimServiceClient, bank bankv1connect.BankServiceClient, drivers ...*Driver) *Fleet {
	return &Fleet{sim: sim, bank: bank, drivers: drivers}
}

func (f *Fleet) Drivers() []*Driver { return f.drivers }

// OnHired routes an outcome from the shared queue to the workplace that claimed
// the pip. Outcomes for a workplace this gateway does not drive are ignored.
func (f *Fleet) OnHired(pipID, workplaceID uint64) {
	for _, d := range f.drivers {
		if d.ID() == workplaceID {
			d.OnHired(pipID)
			return
		}
	}
	slog.Warn("hire outcome for an unknown workplace",
		"pip", pipID, "workplace", workplaceID)
}

// Describe reports every workplace as it sees itself, for JoinWorld.
func (f *Fleet) Describe(ctx context.Context) []*workplacev1.DescribeResponse {
	out := make([]*workplacev1.DescribeResponse, 0, len(f.drivers))
	for _, d := range f.drivers {
		desc, err := d.Describe(ctx)
		if err != nil {
			slog.Warn("could not describe a workplace", "addr", d.addr, "err", err)
			continue
		}
		out = append(out, desc)
	}
	return out
}

// KeepRegistered keeps every workplace on the map.
func (f *Fleet) KeepRegistered(ctx context.Context, every time.Duration) {
	for _, d := range f.drivers {
		go d.KeepRegistered(ctx, every)
	}
}

// Run drives one cycle per interval until the context is cancelled.
//
// Deliberately slower than the tick rate. A shift's effect is applied once a
// second rather than ten times, which keeps the RPC volume proportional to the
// number of workers instead of to the simulation's clock — and the numbers the
// workplaces return are sized for that cadence.
func (f *Fleet) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// A deadline per cycle, so one unresponsive peer costs a round
			// rather than the loop. Cutting a cycle short is cheap: a shift is
			// paid for elapsed ticks, so a pip skipped now is paid in full next
			// time.
			cycleCtx, cancel := context.WithTimeout(ctx, 4*interval)
			err := f.cycle(cycleCtx)
			cancel()
			if err != nil {
				// The world keeps turning without us; log and retry next tick
				// rather than tearing the gateway down.
				slog.Warn("economy cycle failed", "err", err)
			}
		}
	}
}

func (f *Fleet) cycle(ctx context.Context) error {
	snap, err := f.sim.Snapshot(ctx, connect.NewRequest(&simv1.SnapshotRequest{}))
	if err != nil {
		return err
	}
	tick := snap.Msg.GetTick()

	// Which driver, if any, employs each pip — as sim-core sees it. This is the
	// authority: it survives a gateway restart, and it is already what decides
	// whether the pip walks to the building.
	live := make([]*Driver, 0, len(f.drivers))
	byID := make(map[uint64]*Driver, len(f.drivers))
	employedBy := make(map[uint64]map[uint64]bool, len(f.drivers))
	for _, d := range f.drivers {
		id := d.ID()
		if id == 0 {
			// Has not introduced itself yet. Skipping beats guessing an id.
			continue
		}
		live = append(live, d)
		byID[id] = d
		employedBy[id] = make(map[uint64]bool)
	}
	if len(live) == 0 {
		return nil
	}

	for _, p := range snap.Msg.GetPips() {
		if set, ours := employedBy[p.GetEmployerWorkplaceId()]; ours {
			set[p.GetId()] = true
		}
	}

	// Anyone who was employed somewhere and is not any more gets their shift
	// ended now rather than when the workplace's lease expires.
	for _, d := range live {
		for _, pip := range d.syncEmployment(employedBy[d.ID()]) {
			d.endShift(ctx, pip, tick, "no longer employed here")
		}
	}

	// Rotate which workplace gets first refusal this round.
	f.mu.Lock()
	start := f.next
	f.next++
	f.mu.Unlock()

	offersLeft := make(map[uint64]int, len(live))
	for _, d := range live {
		offersLeft[d.ID()] = maxOffersPerRound
	}

	// Wages owed this cycle, per workplace. Paid in one BatchTransfer per
	// workplace after the loop below, not one Transfer per pip as it is
	// discovered — see payWages.
	credits := make(map[uint64]map[uint64]int64, len(live))

	worked, offered, commuting := 0, 0, 0
	for _, p := range snap.Msg.GetPips() {
		if d, employed := byID[p.GetEmployerWorkplaceId()]; employed {
			// Physically at work, as opposed to merely on the payroll. A hired
			// pip walks to the building and may queue at a full door; until it
			// is inside, the shift is kept alive but pays nothing.
			inside := p.GetInsideWorkplaceId() == d.ID()
			if !inside {
				commuting++
			}
			ok, wage := d.work(ctx, p.GetId(), tick, inside)
			if ok && inside {
				worked++
				if wage > 0 {
					if credits[d.ID()] == nil {
						credits[d.ID()] = make(map[uint64]int64)
					}
					credits[d.ID()][p.GetId()] = wage
				}
			}
			continue
		}

		// Unemployed: offered to exactly one workplace this round. Offering the
		// same pip to all of them at once would have two accept it, and the
		// second hire would silently evict the first.
		for i := range live {
			d := live[(start+i)%len(live)]
			if offersLeft[d.ID()] <= 0 {
				continue
			}
			if d.offer(ctx, p.GetId(), tick) {
				offersLeft[d.ID()]--
				offered++
				break
			}
		}
	}

	for workplaceID, pipCredits := range credits {
		f.payWages(ctx, workplaceID, tick, pipCredits)
	}

	if offered > 0 || worked > 0 || commuting > 0 {
		slog.Info("economy cycle", "tick", tick,
			"workplaces", len(live), "offered", offered,
			"worked", worked, "commuting", commuting)
	}
	return nil
}
