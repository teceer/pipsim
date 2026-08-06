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

	// Who this driver saw employed last cycle. Not the authority — sim-core is,
	// and every cycle rebuilds this from the snapshot. It is kept only to spot
	// who *stopped* being employed, so their shift can be ended promptly rather
	// than waiting for the farm's lease to expire.
	//
	// Deriving it rather than owning it is what makes a gateway restart
	// self-healing. Owning it meant a restarted gateway knew nobody, re-offered
	// pips the farm already employed, had the offers rejected, and left them
	// standing inside a building forever holding a place.
	mu       sync.Mutex
	employed map[uint64]bool

	// When a hire was recorded. sim-core applies the intent on the next tick,
	// which can land after the next snapshot is taken — without this grace the
	// cycle would see a just-hired pip as unemployed and cancel its shift.
	hiredAt map[uint64]time.Time

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
		hiredAt:   make(map[uint64]time.Time),
		pending:   make(map[uint64]time.Time),
	}
}

// How long after a hire the driver still believes it, even if the snapshot has
// not caught up.
const hireGrace = 5 * time.Second

// Register puts the workplace on the map, using the numbers the workplace
// reports about itself.
//
// Note what is not happening here: the gateway does not decide the capacity, it
// copies it. `max_workers` has exactly one owner — the workplace service — and
// sim-core enforces it physically so that a building cannot hold more bodies
// than it employs. Registering is idempotent, so calling it on every reconnect
// is the intended usage rather than a compromise.
func (d *Driver) Register(ctx context.Context) error {
	desc, err := d.workplace.Describe(ctx, connect.NewRequest(&workplacev1.DescribeRequest{
		WorkplaceId: d.id,
	}))
	if err != nil {
		return err
	}

	if _, err := d.sim.SubmitIntent(ctx, connect.NewRequest(&simv1.SubmitIntentRequest{
		Intent: &simv1.SubmitIntentRequest_RegisterWorkplace{
			RegisterWorkplace: &simv1.RegisterWorkplaceIntent{
				WorkplaceId: desc.Msg.GetWorkplaceId(),
				Kind:        desc.Msg.GetKind(),
				Position:    desc.Msg.GetPosition(),
				Capacity:    uint32(desc.Msg.GetMaxWorkers()),
			},
		},
	})); err != nil {
		return err
	}

	slog.Info("workplace registered",
		"workplace", desc.Msg.GetWorkplaceId(),
		"kind", desc.Msg.GetKind(),
		"capacity", desc.Msg.GetMaxWorkers(),
		"position", desc.Msg.GetPosition().String())
	return nil
}

// Describe reports the workplace as the workplace sees itself, for JoinWorld.
func (d *Driver) Describe(ctx context.Context) (*workplacev1.DescribeResponse, error) {
	res, err := d.workplace.Describe(ctx, connect.NewRequest(&workplacev1.DescribeRequest{
		WorkplaceId: d.id,
	}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
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
	d.hiredAt[pipID] = time.Now()
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

	// Employment as sim-core sees it. This is the authority: it survives a
	// gateway restart, and it is already what decides whether the pip walks to
	// the building.
	employed := make(map[uint64]bool, len(snap.Msg.GetPips()))
	for _, p := range snap.Msg.GetPips() {
		if p.GetEmployerWorkplaceId() == d.id {
			employed[p.GetId()] = true
		}
	}

	// Anyone who was employed here and is not any more: died, was let go, or
	// vanished with a restarted world. Their shift is ended so the farm frees
	// the position now rather than fifteen seconds from now.
	d.mu.Lock()
	for pip := range d.hiredAt {
		if time.Since(d.hiredAt[pip]) > hireGrace {
			delete(d.hiredAt, pip)
		} else {
			employed[pip] = true
		}
	}
	var gone []uint64
	for pip := range d.employed {
		if !employed[pip] {
			gone = append(gone, pip)
		}
	}
	d.employed = employed
	d.mu.Unlock()

	for _, pip := range gone {
		d.endShift(ctx, pip, tick, "no longer employed here")
	}

	worked, offered, commuting := 0, 0, 0
	for _, p := range snap.Msg.GetPips() {
		if employed[p.GetId()] {
			// Physically at work, as opposed to merely on the payroll. A hired
			// pip walks to the building and may queue at a full door; until it
			// is inside, the shift is kept alive but pays nothing.
			inside := p.GetInsideWorkplaceId() == d.id
			if !inside {
				commuting++
			}
			if d.work(ctx, p.GetId(), tick, inside) && inside {
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

	if offered > 0 || worked > 0 || commuting > 0 {
		slog.Info("economy cycle",
			"tick", tick, "offered", offered, "worked", worked,
			"commuting", commuting, "employed", employedCount)
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

// work exercises the shift. `inside` says whether the pip is actually in the
// building.
//
// The call is made either way, and that is deliberate: Work renews the farm's
// lease, and a commute across the map takes longer than the lease lasts, so
// skipping the call while a pip walks would have the farm reap a shift the pip
// is on its way to. Only the *effects* are withheld — and because the farm
// prices by elapsed ticks, the walk is not paid retroactively either.
func (d *Driver) work(ctx context.Context, pip, tick uint64, inside bool) bool {
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
	if len(deltas) == 0 || !inside {
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

	// Tell the world too, or the pip stays standing in the building forever
	// holding a place nobody can use. Best-effort: the pip may already be dead,
	// in which case sim-core has freed the place itself.
	if _, err := d.sim.SubmitIntent(ctx, connect.NewRequest(&simv1.SubmitIntentRequest{
		Intent: &simv1.SubmitIntentRequest_EndEmployment{
			EndEmployment: &simv1.EndEmploymentIntent{PipId: pip},
		},
	})); err != nil {
		slog.Warn("could not record the end of employment", "pip", pip, "err", err)
	}

	d.mu.Lock()
	delete(d.employed, pip)
	delete(d.pending, pip)
	d.mu.Unlock()
}
