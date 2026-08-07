package farm

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"

	"connectrpc.com/connect"

	workplacev1 "github.com/teceer/pipsim/gen/go/pips/workplace/v1"
)

// Host owns every farm this process runs.
//
// The service is a *kind* of building; the buildings themselves are data. That
// is the whole point of the change: a second farm used to mean a second Helm
// release carrying a different WORKPLACE_ID, which made the count of buildings
// a property of the deployment rather than of the world.
//
// It holds a map rather than one Service, and routes on the `workplace_id`
// every RPC in the contract already carries. That field was decorative while a
// process hosted one building — the answer was always about the same farm.
type Host struct {
	mu        sync.RWMutex
	buildings map[uint64]*Service

	// Which building gets first refusal on the next offer. Without it the
	// lowest id would fill up before the next one saw a candidate, and the
	// order of an environment variable would decide where pips work.
	next int
}

func NewHost(buildings ...*Service) *Host {
	h := &Host{buildings: make(map[uint64]*Service, len(buildings))}
	for _, b := range buildings {
		h.buildings[b.id] = b
	}
	return h
}

// Spec is one building as configuration names it.
//
// Just an id: where the building stands is not this service's business, only
// sim-core's — see ADR 0008. The gateway supplies position separately when it
// registers the building.
type Spec struct {
	ID uint64
}

// ParseSpecs reads the multi-building form: "1,3".
//
// Deliberately strict. A typo here used to be survivable because there was one
// building; with several of them, silently dropping one means a building that
// never appears and an economy that is quietly smaller than intended.
func ParseSpecs(raw string) ([]Spec, error) {
	var out []Spec
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseUint(part, 10, 64)
		if err != nil || id == 0 {
			return nil, fmt.Errorf("bad workplace id in %q", part)
		}
		out = append(out, Spec{ID: id})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no buildings configured")
	}
	return out, nil
}

// ids returns the hosted ids in a stable order, so List and offer rotation are
// reproducible rather than depending on map iteration.
func (h *Host) ids() []uint64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]uint64, 0, len(h.buildings))
	for id := range h.buildings {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// resolve finds the building an RPC is addressed to.
//
// A zero id is accepted only when this process hosts exactly one building. That
// is not politeness towards old callers: it is what lets a single-building
// service keep answering `Describe{}` the way the conformance suite and the
// pre-List gateway expect, while a multi-building one refuses to guess.
func (h *Host) resolve(id uint64) (*Service, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if id == 0 {
		if len(h.buildings) == 1 {
			for _, b := range h.buildings {
				return b, nil
			}
		}
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("workplace_id is required: this service hosts %d buildings",
				len(h.buildings)))
	}

	b, ok := h.buildings[id]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("no workplace %d here", id))
	}
	return b, nil
}

func (h *Host) List(
	ctx context.Context,
	_ *connect.Request[workplacev1.ListRequest],
) (*connect.Response[workplacev1.ListResponse], error) {
	ids := h.ids()
	out := make([]*workplacev1.DescribeResponse, 0, len(ids))
	for _, id := range ids {
		b, err := h.resolve(id)
		if err != nil {
			return nil, err
		}
		res, err := b.Describe(ctx, connect.NewRequest(&workplacev1.DescribeRequest{
			WorkplaceId: id,
		}))
		if err != nil {
			return nil, err
		}
		out = append(out, res.Msg)
	}
	return connect.NewResponse(&workplacev1.ListResponse{Workplaces: out}), nil
}

func (h *Host) Describe(
	ctx context.Context,
	req *connect.Request[workplacev1.DescribeRequest],
) (*connect.Response[workplacev1.DescribeResponse], error) {
	b, err := h.resolve(req.Msg.GetWorkplaceId())
	if err != nil {
		return nil, err
	}
	return b.Describe(ctx, req)
}

func (h *Host) CanEmploy(
	ctx context.Context,
	req *connect.Request[workplacev1.CanEmployRequest],
) (*connect.Response[workplacev1.CanEmployResponse], error) {
	b, err := h.resolve(req.Msg.GetWorkplaceId())
	if err != nil {
		return nil, err
	}
	return b.CanEmploy(ctx, req)
}

func (h *Host) StartShift(
	ctx context.Context,
	req *connect.Request[workplacev1.StartShiftRequest],
) (*connect.Response[workplacev1.StartShiftResponse], error) {
	b, err := h.resolve(req.Msg.GetWorkplaceId())
	if err != nil {
		return nil, err
	}
	return b.StartShift(ctx, req)
}

func (h *Host) Work(
	ctx context.Context,
	req *connect.Request[workplacev1.WorkRequest],
) (*connect.Response[workplacev1.WorkResponse], error) {
	b, err := h.resolve(req.Msg.GetWorkplaceId())
	if err != nil {
		return nil, err
	}
	return b.Work(ctx, req)
}

func (h *Host) EndShift(
	ctx context.Context,
	req *connect.Request[workplacev1.EndShiftRequest],
) (*connect.Response[workplacev1.EndShiftResponse], error) {
	b, err := h.resolve(req.Msg.GetWorkplaceId())
	if err != nil {
		return nil, err
	}
	return b.EndShift(ctx, req)
}

func (h *Host) Buy(
	ctx context.Context,
	req *connect.Request[workplacev1.BuyRequest],
) (*connect.Response[workplacev1.BuyResponse], error) {
	b, err := h.resolve(req.Msg.GetWorkplaceId())
	if err != nil {
		return nil, err
	}
	return b.Buy(ctx, req)
}

// ConsiderOffer answers a work offer for this *kind* of building.
//
// The queue is per kind, not per building, so an offer arriving here is not
// addressed to any particular farm — this picks one. Buildings are tried in
// rotation from a moving start, and the first to claim wins. Trying them in a
// fixed order would fill the lowest id first and leave the rest empty until it
// was full, which is a scheduling policy nobody chose.
func (h *Host) ConsiderOffer(ctx context.Context, pipID, tick uint64) (bool, string) {
	ids := h.ids()
	if len(ids) == 0 {
		return false, "no buildings"
	}

	h.mu.Lock()
	start := h.next
	h.next++
	h.mu.Unlock()

	lastReason := "no free positions"
	for i := range ids {
		id := ids[(start+i)%len(ids)]
		b, err := h.resolve(id)
		if err != nil {
			continue
		}
		accepted, reason := b.ConsiderOffer(ctx, pipID, tick)
		if accepted {
			return true, ""
		}
		if reason != "" {
			lastReason = reason
		}
	}
	return false, lastReason
}

// Workers reports the headcount across every building, for /healthz.
func (h *Host) Workers() int {
	total := 0
	for _, id := range h.ids() {
		b, err := h.resolve(id)
		if err != nil {
			continue
		}
		n := b.Workers()
		if n < 0 {
			slog.Warn("could not count workers", "workplace", id)
			continue
		}
		total += n
	}
	return total
}
