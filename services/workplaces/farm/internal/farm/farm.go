// Package farm implements pips.workplace.v1.WorkplaceService.
//
// It is the first workplace, and the template for the rest: five RPCs, no
// knowledge that any other workplace exists, and no pip state beyond who is on
// shift right now. Pips belong to sim-core, and shifts are held on a lease:
// one that stops being worked expires by itself, so this service never has to
// ask anyone who still exists.
//
// What the farm decides, and nothing else does: how many it employs, how much
// grain a shift yields, and what that shift does to a worker's needs. sim-core
// applies those need deltas — clamped, because a workplace is a separate
// service that may be wrong — and it is sim-core that owns whether the pip
// lives or dies.
package farm

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"connectrpc.com/connect"

	simv1 "github.com/teceer/pipsim/gen/go/pips/sim/v1"
	workplacev1 "github.com/teceer/pipsim/gen/go/pips/workplace/v1"
)

const (
	// Deliberately smaller than the population. Scarcity is the interesting
	// case: with room for everyone there is no allocation problem, and the
	// competing-consumer queue this is heading towards would be pointless.
	MaxWorkers = 24

	// Per simulation tick, not per Work call.
	//
	// The distinction cost a debugging session: the driver batches, calling
	// Work once a second while the world ticks ten times, so returning these
	// as flat per-call amounts made working *worse* than idling — a worker
	// drained 2 food per tick and was handed 12 back per second. Work now
	// scales by the ticks actually elapsed, which makes the contract
	// independent of whatever cadence a caller chooses.
	grainPerTick = 3

	// What a shift does to the worker, keyed by pips.sim.v1.Need.
	//
	// Farm work feeds you — this is a subsistence farm, not a market — so the
	// loop closes without a storage or trading service existing yet. Rest is
	// the cost. When a granary and a market land, food stops being conjured
	// here and starts being drawn from stock.
	needFood = 1
	needRest = 2

	// Must exceed the world's drain on a working pip, or employment is a
	// slower death rather than a living.
	foodPerTick = 5
	restPerTick = -1

	// One Work call is never credited for more than this, so a driver that
	// stalls and resumes cannot hand out a windfall.
	maxTicksPerWork = 40

	// A shift nobody has asked to Work for this long is reaped.
	//
	// This is the fix for a real failure seen in the cluster: sim-core restarted
	// with a fresh world, and the farm went on believing it employed 24 pips
	// that no longer existed. Every position was taken by a ghost and nobody
	// could ever be hired again.
	//
	// A lease rather than a reconciliation RPC, because the workplace should not
	// have to ask sim-core about pips — it is not the workplace's business who
	// exists. If the shift stops being exercised, it stops being real.
	shiftLease = 15 * time.Second
)

type shift struct {
	startedTick  uint64
	lastWorkTick uint64
	lastWork     time.Time
}

// Service tracks who is on shift. Not persisted: pips belong to sim-core.
type Service struct {
	mu       sync.RWMutex
	onShift  map[uint64]*shift
	id       uint64
	position *simv1.Vec2
	now      func() time.Time
}

func New(workplaceID uint64, x, y int32) *Service {
	return &Service{
		onShift:  make(map[uint64]*shift),
		id:       workplaceID,
		position: &simv1.Vec2{XMilli: x, YMilli: y},
		now:      time.Now,
	}
}

// reap drops shifts whose lease has expired. Callers must hold the write lock.
func (s *Service) reapLocked() {
	for pip, sh := range s.onShift {
		if s.now().Sub(sh.lastWork) > shiftLease {
			delete(s.onShift, pip)
			slog.Info("shift lease expired", "pip", pip, "workers", len(s.onShift))
		}
	}
}

func (s *Service) Describe(
	_ context.Context,
	_ *connect.Request[workplacev1.DescribeRequest],
) (*connect.Response[workplacev1.DescribeResponse], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return connect.NewResponse(&workplacev1.DescribeResponse{
		WorkplaceId:    s.id,
		Kind:           "farm",
		DisplayName:    "Farm",
		MaxWorkers:     MaxWorkers,
		CurrentWorkers: int32(len(s.onShift)),
		Position:       s.position,
		Produces:       []workplacev1.ResourceKind{workplacev1.ResourceKind_RESOURCE_KIND_GRAIN},
	}), nil
}

func (s *Service) CanEmploy(
	_ context.Context,
	req *connect.Request[workplacev1.CanEmployRequest],
) (*connect.Response[workplacev1.CanEmployResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Reap first: a full-looking workplace may be full of expired leases.
	s.reapLocked()

	if _, already := s.onShift[req.Msg.GetPipId()]; already {
		return connect.NewResponse(&workplacev1.CanEmployResponse{
			Allowed: false,
			Reason:  "already on shift here",
		}), nil
	}
	if len(s.onShift) >= MaxWorkers {
		return connect.NewResponse(&workplacev1.CanEmployResponse{
			Allowed: false,
			Reason:  "no free positions",
		}), nil
	}
	return connect.NewResponse(&workplacev1.CanEmployResponse{Allowed: true}), nil
}

func (s *Service) StartShift(
	_ context.Context,
	req *connect.Request[workplacev1.StartShiftRequest],
) (*connect.Response[workplacev1.StartShiftResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reapLocked()

	// Re-checked under the write lock. CanEmploy is advisory: between that call
	// and this one another caller may have taken the last position.
	if len(s.onShift) >= MaxWorkers {
		return connect.NewResponse(&workplacev1.StartShiftResponse{
			Accepted: false,
			Reason:   "no free positions",
		}), nil
	}

	s.onShift[req.Msg.GetPipId()] = &shift{
		startedTick:  req.Msg.GetTick(),
		lastWorkTick: req.Msg.GetTick(),
		lastWork:     s.now(),
	}
	slog.Info("shift started",
		"pip", req.Msg.GetPipId(),
		"tick", req.Msg.GetTick(),
		"workers", len(s.onShift))

	return connect.NewResponse(&workplacev1.StartShiftResponse{Accepted: true}), nil
}

func (s *Service) Work(
	_ context.Context,
	req *connect.Request[workplacev1.WorkRequest],
) (*connect.Response[workplacev1.WorkResponse], error) {
	// Work renews the lease, which is what makes an abandoned shift expire.
	tick := req.Msg.GetTick()

	s.mu.Lock()
	sh, working := s.onShift[req.Msg.GetPipId()]
	var elapsed int32
	if working {
		if tick > sh.lastWorkTick {
			elapsed = int32(min(tick-sh.lastWorkTick, maxTicksPerWork))
		}
		sh.lastWorkTick = tick
		sh.lastWork = s.now()
	}
	s.mu.Unlock()

	if !working {
		// Not an error: sim-core may have decided the pip is dead or has left,
		// and the caller finding out here is the normal way that surfaces.
		return connect.NewResponse(&workplacev1.WorkResponse{ShiftShouldEnd: true}), nil
	}

	if elapsed == 0 {
		// Called twice within the same tick. Paying again would let a chatty
		// caller mint food.
		return connect.NewResponse(&workplacev1.WorkResponse{}), nil
	}

	return connect.NewResponse(&workplacev1.WorkResponse{
		Produced: []*workplacev1.ResourceAmount{{
			Kind:   workplacev1.ResourceKind_RESOURCE_KIND_GRAIN,
			Amount: grainPerTick * elapsed,
		}},
		NeedDeltas: map[int32]int32{
			needFood: foodPerTick * elapsed,
			needRest: restPerTick * elapsed,
		},
	}), nil
}

func (s *Service) EndShift(
	_ context.Context,
	req *connect.Request[workplacev1.EndShiftRequest],
) (*connect.Response[workplacev1.EndShiftResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sh, ok := s.onShift[req.Msg.GetPipId()]; ok {
		delete(s.onShift, req.Msg.GetPipId())
		slog.Info("shift ended",
			"pip", req.Msg.GetPipId(),
			"reason", req.Msg.GetReason(),
			"ticks_worked", req.Msg.GetTick()-sh.startedTick,
			"workers", len(s.onShift))
	}
	return connect.NewResponse(&workplacev1.EndShiftResponse{}), nil
}

// Workers reports the current headcount, for the health endpoint.
func (s *Service) Workers() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.onShift)
}
