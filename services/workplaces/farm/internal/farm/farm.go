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
//
// Where the shifts are *kept* is a Store (see store.go). That indirection is
// not architecture for its own sake: with the state in each replica's memory,
// two replicas held 24 and 13 shifts while the gateway believed in 24.
package farm

import (
	"context"
	"log/slog"
	"strconv"
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

	// What a shift here pays, per tick worked.
	//
	// This replaced the food a shift used to hand out. The farm no longer has
	// any opinion about hunger — it sells food and pays wages, and what food
	// does to a pip is sim-core's to know (ADR 0006). The tavern shipping a
	// number that starved its own staff was only possible while every
	// building had to describe the world's metabolism to describe itself.
	wagePerTick = 6

	// What a pip pays for one meal.
	//
	// Well under what a shift earns in the time one meal lasts: a meal
	// restores 200 food, a working pip burns 2 a tick, so it buys roughly a
	// hundred ticks of work — six hundred in wages against fifty at the till.
	// Employment has to be comfortably survivable, or the interesting failure
	// (a wage that cannot cover food) is the only one anyone ever sees.
	foodPrice = 50

	// How demanding the work is. A scalar the farm declares about itself;
	// sim-core, not the farm, decides what effort costs.
	effort = 1

	// One Work call is never credited for more than this, so a driver that
	// stalls and resumes cannot hand out a windfall.
	MaxTicksPerWork = 40

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
	ShiftLease = 15 * time.Second
)

// Service answers the workplace contract. Shift state lives in the Store.
type Service struct {
	store    Store
	id       uint64
	position *simv1.Vec2
}

// New builds a farm holding its shifts in memory — correct at one replica.
func New(workplaceID uint64, x, y int32) *Service {
	return NewWithStore(
		newMemStore(MaxWorkers, ShiftLease, MaxTicksPerWork, time.Now),
		workplaceID, x, y,
	)
}

func NewWithStore(store Store, workplaceID uint64, x, y int32) *Service {
	return &Service{
		store:    store,
		id:       workplaceID,
		position: &simv1.Vec2{XMilli: x, YMilli: y},
	}
}

func (s *Service) Describe(
	ctx context.Context,
	_ *connect.Request[workplacev1.DescribeRequest],
) (*connect.Response[workplacev1.DescribeResponse], error) {
	workers, err := s.store.Count(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}

	return connect.NewResponse(&workplacev1.DescribeResponse{
		WorkplaceId: s.id,
		Kind:        "farm",
		// Carries the id: several farms now share a process, and "Farm" three
		// times over is unreadable in a log line or on the map.
		DisplayName:    "Farm #" + strconv.FormatUint(s.id, 10),
		MaxWorkers:     MaxWorkers,
		CurrentWorkers: int32(workers),
		Position:       s.position,
		Produces:       []workplacev1.ResourceKind{workplacev1.ResourceKind_RESOURCE_KIND_GRAIN},
		Wage:           wagePerTick,
		Effort:         effort,
		Sells: []*workplacev1.Offer{{
			Kind:  workplacev1.ResourceKind_RESOURCE_KIND_FOOD,
			Price: foodPrice,
		}},
	}), nil
}

// CanEmploy is advisory and always has been: it reports headroom, and the
// position is only actually taken by StartShift or ConsiderOffer.
func (s *Service) CanEmploy(
	ctx context.Context,
	_ *connect.Request[workplacev1.CanEmployRequest],
) (*connect.Response[workplacev1.CanEmployResponse], error) {
	workers, err := s.store.Count(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	if workers >= MaxWorkers {
		return connect.NewResponse(&workplacev1.CanEmployResponse{
			Allowed: false,
			Reason:  "no free positions",
		}), nil
	}
	return connect.NewResponse(&workplacev1.CanEmployResponse{Allowed: true}), nil
}

func (s *Service) StartShift(
	ctx context.Context,
	req *connect.Request[workplacev1.StartShiftRequest],
) (*connect.Response[workplacev1.StartShiftResponse], error) {
	accepted, reason, err := s.store.Claim(ctx, req.Msg.GetPipId(), req.Msg.GetTick())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	if !accepted {
		return connect.NewResponse(&workplacev1.StartShiftResponse{
			Accepted: false,
			Reason:   reason,
		}), nil
	}

	slog.Info("shift started", "pip", req.Msg.GetPipId(), "tick", req.Msg.GetTick())
	return connect.NewResponse(&workplacev1.StartShiftResponse{Accepted: true}), nil
}

func (s *Service) Work(
	ctx context.Context,
	req *connect.Request[workplacev1.WorkRequest],
) (*connect.Response[workplacev1.WorkResponse], error) {
	// Work renews the lease, which is what makes an abandoned shift expire.
	elapsed, working, err := s.store.Touch(ctx, req.Msg.GetPipId(), req.Msg.GetTick())
	if err != nil {
		// Deliberately an error rather than shift_should_end. A store blip is
		// not evidence the pip has left, and answering "end the shift" would
		// have every worker fired the moment Redis hiccups.
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}

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
		Wage: int64(wagePerTick * elapsed),
	}), nil
}

func (s *Service) EndShift(
	ctx context.Context,
	req *connect.Request[workplacev1.EndShiftRequest],
) (*connect.Response[workplacev1.EndShiftResponse], error) {
	started, found, err := s.store.Release(ctx, req.Msg.GetPipId(), req.Msg.GetTick())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	if found {
		slog.Info("shift ended",
			"pip", req.Msg.GetPipId(),
			"reason", req.Msg.GetReason(),
			"ticks_worked", req.Msg.GetTick()-started)
	}
	return connect.NewResponse(&workplacev1.EndShiftResponse{}), nil
}

// Buy sells one meal.
//
// The farm is where food comes from, so it is where food is bought. It sells
// FOOD rather than the GRAIN it produces: turning one into the other is a
// mill's job, and there is no mill — when there is, the farm sells grain to
// it and stops selling meals.
//
// What the meal does to the pip is not answered here, deliberately. This
// returns a price; sim-core owns what FOOD does to a body.
func (s *Service) Buy(
	_ context.Context,
	req *connect.Request[workplacev1.BuyRequest],
) (*connect.Response[workplacev1.BuyResponse], error) {
	if req.Msg.GetKind() != workplacev1.ResourceKind_RESOURCE_KIND_FOOD {
		return connect.NewResponse(&workplacev1.BuyResponse{
			Ok:     false,
			Reason: "the farm sells only food",
		}), nil
	}
	return connect.NewResponse(&workplacev1.BuyResponse{
		Ok:    true,
		Price: foodPrice,
	}), nil
}

// ConsiderOffer answers a work offer taken off the queue.
//
// Capacity check and shift start are one atomic operation in the store rather
// than separate CanEmploy/StartShift calls. That matters more now than it did
// in memory: with several replicas sharing one store, "is there room" and "take
// the room" have to be indivisible or two consumers race for the last position.
func (s *Service) ConsiderOffer(ctx context.Context, pipID, tick uint64) (bool, string) {
	accepted, reason, err := s.store.Claim(ctx, pipID, tick)
	if err != nil {
		// Requeued by the consumer: a store failure is not a rejection.
		slog.Warn("could not claim a position", "pip", pipID, "err", err)
		return false, "store unavailable"
	}
	if accepted {
		slog.Info("offer accepted", "pip", pipID, "tick", tick)
	}
	return accepted, reason
}

// Workers reports the current headcount, for the health endpoint.
func (s *Service) Workers() int {
	n, err := s.store.Count(context.Background())
	if err != nil {
		return -1
	}
	return n
}
