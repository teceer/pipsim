// Package economy drives the loop between sim-core and the workplaces.
//
// The split it is careful about: this package contains *mechanics*, not policy.
// It fills empty positions and forwards the consequences of a shift; it does
// not decide who deserves work, what a shift costs, or whether a pip survives.
// The farm owns the first, sim-core owns the rest.
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

	simv1 "github.com/teceer/pipsim/gen/go/pips/sim/v1"
	"github.com/teceer/pipsim/gen/go/pips/sim/v1/simv1connect"
	workplacev1 "github.com/teceer/pipsim/gen/go/pips/workplace/v1"
	"github.com/teceer/pipsim/gen/go/pips/workplace/v1/workplacev1connect"
)

type Driver struct {
	sim       simv1connect.SimServiceClient
	workplace workplacev1connect.WorkplaceServiceClient
	id        uint64
	kind      string
	offers    *Publisher

	// Who this driver believes is on shift. Written by the outcome consumer as
	// well as the cycle, hence the lock. Rebuilt from nothing on restart —
	// sim-core is the authority on pips, and a stale view heals within a cycle
	// because Work reports an unknown pip as shift-should-end.
	mu       sync.Mutex
	employed map[uint64]bool

	// Offered but not yet answered. Without this the driver would re-offer the
	// same pip every round while its first offer is still queued, and a
	// workplace with one free position would be handed the same candidate a
	// dozen times.
	pending map[uint64]time.Time
}

func NewDriver(
	sim simv1connect.SimServiceClient,
	workplace workplacev1connect.WorkplaceServiceClient,
	workplaceID uint64,
	kind string,
	offers *Publisher,
) *Driver {
	return &Driver{
		sim:       sim,
		workplace: workplace,
		id:        workplaceID,
		kind:      kind,
		offers:    offers,
		employed:  make(map[uint64]bool),
		pending:   make(map[uint64]time.Time),
	}
}

// OnHired records a workplace's acceptance and tells the world about it.
//
// This is the only place a Hire intent is submitted. The workplace decided; the
// gateway records. Keeping that split is what lets a workplace stay ignorant of
// sim-core's existence.
func (d *Driver) OnHired(pipID, workplaceID uint64) {
	if _, err := d.sim.SubmitIntent(context.Background(), connect.NewRequest(&simv1.SubmitIntentRequest{
		Intent: &simv1.SubmitIntentRequest_Hire{
			Hire: &simv1.HireIntent{PipId: pipID, WorkplaceId: workplaceID},
		},
	})); err != nil {
		slog.Warn("could not record hire", "pip", pipID, "err", err)
		return
	}

	d.mu.Lock()
	d.employed[pipID] = true
	delete(d.pending, pipID)
	d.mu.Unlock()
}

// Run drives one cycle per interval until the context is cancelled.
//
// Deliberately slower than the tick rate. A shift's effect is applied once a
// second rather than ten times, which keeps the RPC volume proportional to the
// number of workers instead of to the simulation's clock — and the numbers the
// farm returns are sized for that cadence.
func (d *Driver) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// A deadline per cycle, so one unresponsive peer costs a round
			// rather than the loop. Cutting a cycle short is cheap: Work pays
			// for elapsed ticks, so a pip skipped now is paid in full next
			// time.
			cycleCtx, cancel := context.WithTimeout(ctx, 4*interval)
			err := d.cycle(cycleCtx)
			cancel()
			if err != nil {
				// The world keeps turning without us; log and retry next tick
				// rather than tearing the gateway down.
				slog.Warn("economy cycle failed", "err", err)
			}
		}
	}
}

func (d *Driver) cycle(ctx context.Context) error {
	snap, err := d.sim.Snapshot(ctx, connect.NewRequest(&simv1.SnapshotRequest{}))
	if err != nil {
		return err
	}
	tick := snap.Msg.GetTick()

	alive := make(map[uint64]bool, len(snap.Msg.GetPips()))
	for _, p := range snap.Msg.GetPips() {
		alive[p.GetId()] = true
	}

	// Pips that died on shift. The farm frees the position; without this a
	// starved worker would occupy it forever.
	d.mu.Lock()
	var dead []uint64
	for pip := range d.employed {
		if !alive[pip] {
			dead = append(dead, pip)
		}
	}
	d.mu.Unlock()
	for _, pip := range dead {
		d.endShift(ctx, pip, tick, "pip died")
	}

	worked, offered := 0, 0
	for _, p := range snap.Msg.GetPips() {
		d.mu.Lock()
		isEmployed := d.employed[p.GetId()]
		d.mu.Unlock()

		if isEmployed {
			if d.work(ctx, p.GetId(), tick) {
				worked++
			}
			continue
		}
		if offered < maxOffersPerRound && d.offer(ctx, p.GetId(), tick) {
			offered++
		}
	}

	d.mu.Lock()
	employedCount := len(d.employed)
	d.mu.Unlock()

	if offered > 0 || worked > 0 {
		slog.Info("economy cycle",
			"tick", tick, "offered", offered, "worked", worked, "employed", employedCount)
	}
	return nil
}

// offer publishes a pip to the work exchange. Returns whether one was sent.
func (d *Driver) offer(ctx context.Context, pip, tick uint64) bool {
	d.mu.Lock()
	// An offer nobody answered eventually expires at the broker; give up
	// tracking it a little later so the pip becomes offerable again.
	if at, waiting := d.pending[pip]; waiting && time.Since(at) < 15*time.Second {
		d.mu.Unlock()
		return false
	}
	d.pending[pip] = time.Now()
	d.mu.Unlock()

	if err := d.offers.Offer(ctx, d.kind, pip, tick, ""); err != nil {
		slog.Warn("could not publish offer", "pip", pip, "err", err)
		d.mu.Lock()
		delete(d.pending, pip)
		d.mu.Unlock()
		return false
	}
	return true
}

func (d *Driver) work(ctx context.Context, pip, tick uint64) bool {
	res, err := d.workplace.Work(ctx, connect.NewRequest(&workplacev1.WorkRequest{
		WorkplaceId: d.id,
		PipId:       pip,
		Tick:        tick,
	}))
	if err != nil {
		return false
	}

	if res.Msg.GetShiftShouldEnd() {
		d.endShift(ctx, pip, tick, "workplace ended the shift")
		return false
	}

	deltas := res.Msg.GetNeedDeltas()
	if len(deltas) == 0 {
		return true
	}

	// The workplace says what the shift did; sim-core decides what that means,
	// and clamps it. Nothing here validates the numbers on purpose — putting a
	// second opinion in the middle is how two services end up disagreeing about
	// the rules.
	if _, err := d.sim.SubmitIntent(ctx, connect.NewRequest(&simv1.SubmitIntentRequest{
		Intent: &simv1.SubmitIntentRequest_ApplyNeeds{
			ApplyNeeds: &simv1.ApplyNeedsIntent{PipId: pip, NeedDeltas: deltas},
		},
	})); err != nil {
		return false
	}
	return true
}

func (d *Driver) endShift(ctx context.Context, pip, tick uint64, reason string) {
	_, _ = d.workplace.EndShift(ctx, connect.NewRequest(&workplacev1.EndShiftRequest{
		WorkplaceId: d.id,
		PipId:       pip,
		Tick:        tick,
		Reason:      reason,
	}))
	d.mu.Lock()
	delete(d.employed, pip)
	delete(d.pending, pip)
	d.mu.Unlock()
}
