// Package economy drives the loop between sim-core and the workplaces.
//
// The split it is careful about: this package contains *mechanics*, not policy.
// It fills empty positions and forwards the consequences of a shift; it does
// not decide who deserves work, what a shift costs, or whether a pip survives.
// The farm owns the first, sim-core owns the rest.
//
// Concretely, it never looks at a pip's needs. It hires whoever is unemployed
// until the workplace says it is full, which means scarcity emerges from the
// workplace's own capacity rather than from a rule written here. If this file
// ever grows an `if needs[food] < …`, the boundary has leaked.
package economy

import (
	"context"
	"log/slog"
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

	// Who this driver believes is on shift. Rebuilt from nothing on restart —
	// sim-core is the authority on pips, and a stale local view heals within a
	// cycle because Work reports an unknown pip as shift-should-end.
	employed map[uint64]bool
}

func NewDriver(
	sim simv1connect.SimServiceClient,
	workplace workplacev1connect.WorkplaceServiceClient,
	workplaceID uint64,
) *Driver {
	return &Driver{
		sim:       sim,
		workplace: workplace,
		id:        workplaceID,
		employed:  make(map[uint64]bool),
	}
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
			if err := d.cycle(ctx); err != nil {
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
	for pip := range d.employed {
		if !alive[pip] {
			d.endShift(ctx, pip, tick, "pip died")
		}
	}

	worked, hired := 0, 0
	for _, p := range snap.Msg.GetPips() {
		if d.employed[p.GetId()] {
			if d.work(ctx, p.GetId(), tick) {
				worked++
			}
			continue
		}
		if d.tryHire(ctx, p.GetId(), tick) {
			hired++
		}
	}

	if hired > 0 || worked > 0 {
		slog.Info("economy cycle",
			"tick", tick, "hired", hired, "worked", worked, "employed", len(d.employed))
	}
	return nil
}

// tryHire returns whether the pip started a shift. A full workplace is the
// normal case, not an error.
func (d *Driver) tryHire(ctx context.Context, pip, tick uint64) bool {
	can, err := d.workplace.CanEmploy(ctx, connect.NewRequest(&workplacev1.CanEmployRequest{
		WorkplaceId: d.id,
		PipId:       pip,
	}))
	if err != nil || !can.Msg.GetAllowed() {
		return false
	}

	started, err := d.workplace.StartShift(ctx, connect.NewRequest(&workplacev1.StartShiftRequest{
		WorkplaceId: d.id,
		PipId:       pip,
		Tick:        tick,
	}))
	if err != nil || !started.Msg.GetAccepted() {
		return false
	}

	// Only now does the world learn about it. The workplace tracks the shift;
	// sim-core tracks the pip; this call is what joins them.
	if _, err := d.sim.SubmitIntent(ctx, connect.NewRequest(&simv1.SubmitIntentRequest{
		Intent: &simv1.SubmitIntentRequest_Hire{
			Hire: &simv1.HireIntent{PipId: pip, WorkplaceId: d.id},
		},
	})); err != nil {
		// Roll the shift back rather than leaving the workplace believing it
		// employs someone the world has never heard of.
		d.endShift(ctx, pip, tick, "could not record hire")
		return false
	}

	d.employed[pip] = true
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
	delete(d.employed, pip)
}
