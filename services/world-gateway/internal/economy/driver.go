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

// How long after a hire the driver still believes it, even if the snapshot has
// not caught up.
const hireGrace = 5 * time.Second

// Driver is the gateway's half of one workplace.
//
// It knows an address and nothing else at construction. Who lives there — the
// workplace's id and kind — is answered by the workplace itself on the first
// `Describe`, so the same fact is not configured twice and cannot disagree
// between a Helm value and a service's own environment.
type Driver struct {
	sim       simv1connect.SimServiceClient
	workplace workplacev1connect.WorkplaceServiceClient
	addr      string
	offers    *Publisher

	mu sync.Mutex

	// Zero until the workplace has introduced itself. The fleet skips a driver
	// in that state rather than guessing.
	id   uint64
	kind string

	// Who this driver saw employed last cycle. Not the authority — sim-core is,
	// and every cycle rebuilds this from the snapshot. It is kept only to spot
	// who *stopped* being employed, so their shift can be ended promptly rather
	// than waiting for the workplace's lease to expire.
	//
	// Deriving it rather than owning it is what makes a gateway restart
	// self-healing. Owning it meant a restarted gateway knew nobody, re-offered
	// pips the workplace already employed, had the offers rejected, and left
	// them standing inside a building forever holding a place.
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
	addr string,
	offers *Publisher,
) *Driver {
	return &Driver{
		sim:       sim,
		workplace: workplace,
		addr:      addr,
		offers:    offers,
		employed:  make(map[uint64]bool),
		hiredAt:   make(map[uint64]time.Time),
		pending:   make(map[uint64]time.Time),
	}
}

func (d *Driver) ID() uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.id
}

func (d *Driver) Kind() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.kind
}

// Register puts the workplace on the map, using the numbers it reports about
// itself, and learns its identity in the process.
//
// Note what is not happening here: the gateway does not decide the capacity, it
// copies it. `max_workers` has exactly one owner — the workplace service — and
// sim-core enforces it physically so that a building cannot hold more bodies
// than it employs. Registering is idempotent, so calling it on a loop is the
// intended usage rather than a compromise.
func (d *Driver) Register(ctx context.Context) error {
	desc, err := d.Describe(ctx)
	if err != nil {
		return err
	}

	if _, err := d.sim.SubmitIntent(ctx, connect.NewRequest(&simv1.SubmitIntentRequest{
		Intent: &simv1.SubmitIntentRequest_RegisterWorkplace{
			RegisterWorkplace: &simv1.RegisterWorkplaceIntent{
				WorkplaceId: desc.GetWorkplaceId(),
				Kind:        desc.GetKind(),
				Position:    desc.GetPosition(),
				Capacity:    uint32(desc.GetMaxWorkers()),
			},
		},
	})); err != nil {
		return err
	}

	d.mu.Lock()
	first := d.id == 0
	d.id, d.kind = desc.GetWorkplaceId(), desc.GetKind()
	d.mu.Unlock()

	if first {
		slog.Info("workplace registered",
			"addr", d.addr,
			"workplace", desc.GetWorkplaceId(),
			"kind", desc.GetKind(),
			"capacity", desc.GetMaxWorkers(),
			"position", desc.GetPosition().String())
	}
	return nil
}

// KeepRegistered re-registers forever.
//
// Not just at startup, and not just until it succeeds. Registration is
// idempotent by contract, so repeating it is free, and it covers the case that
// actually happens in this cluster: sim-core restarts with a fresh world and
// forgets every building while the gateway is still running. A world with a
// workplace nobody can find is a world where every hired pip stands still.
func (d *Driver) KeepRegistered(ctx context.Context, every time.Duration) {
	failures := 0
	for ctx.Err() == nil {
		if err := d.Register(ctx); err != nil {
			// Quiet after the first: a workplace that is down stays down for
			// many cycles, and one line per attempt drowns the log.
			if failures == 0 {
				slog.Warn("could not register the workplace", "addr", d.addr, "err", err)
			}
			failures++
		} else {
			failures = 0
		}

		select {
		case <-ctx.Done():
		case <-time.After(every):
		}
	}
}

// Describe reports the workplace as the workplace sees itself.
func (d *Driver) Describe(ctx context.Context) (*workplacev1.DescribeResponse, error) {
	res, err := d.workplace.Describe(ctx, connect.NewRequest(&workplacev1.DescribeRequest{
		WorkplaceId: d.ID(),
	}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

// OnHired records that this workplace took a pip on, and tells the world.
//
// This is the only place a Hire intent is submitted. The workplace decided; the
// gateway records. Keeping that split is what lets a workplace stay ignorant of
// sim-core's existence.
func (d *Driver) OnHired(pipID uint64) {
	if _, err := d.sim.SubmitIntent(context.Background(), connect.NewRequest(&simv1.SubmitIntentRequest{
		Intent: &simv1.SubmitIntentRequest_Hire{
			Hire: &simv1.HireIntent{PipId: pipID, WorkplaceId: d.ID()},
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

// syncEmployment replaces the driver's view with sim-core's and reports who
// dropped off it: died, was let go, or vanished with a restarted world.
func (d *Driver) syncEmployment(employed map[uint64]bool) []uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()

	for pip, at := range d.hiredAt {
		if time.Since(at) > hireGrace {
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
	return gone
}

// offer publishes a pip to the work exchange. Returns whether one was sent.
func (d *Driver) offer(ctx context.Context, pip, tick uint64) bool {
	kind := d.Kind()
	if kind == "" {
		return false
	}

	d.mu.Lock()
	// An offer nobody answered eventually expires at the broker; give up
	// tracking it a little later so the pip becomes offerable again.
	if at, waiting := d.pending[pip]; waiting && time.Since(at) < 15*time.Second {
		d.mu.Unlock()
		return false
	}
	d.pending[pip] = time.Now()
	d.mu.Unlock()

	if err := d.offers.Offer(ctx, kind, pip, tick, ""); err != nil {
		slog.Warn("could not publish offer", "pip", pip, "kind", kind, "err", err)
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
// The call is made either way, and that is deliberate: Work renews the
// workplace's shift lease, and a commute across the map takes longer than the
// lease lasts, so skipping the call while a pip walks would have the workplace
// reap a shift the pip is on its way to. Only the *effects* are withheld — and
// because a shift is priced by elapsed ticks, the walk is not paid
// retroactively either.
func (d *Driver) work(ctx context.Context, pip, tick uint64, inside bool) bool {
	res, err := d.workplace.Work(ctx, connect.NewRequest(&workplacev1.WorkRequest{
		WorkplaceId: d.ID(),
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
		WorkplaceId: d.ID(),
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
