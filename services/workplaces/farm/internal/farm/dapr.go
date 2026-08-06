package farm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	workplacev1 "github.com/teceer/pipsim/gen/go/pips/workplace/v1"
)

// ActorType is the Dapr entity name a farm building is registered under.
const ActorType = "farm"

// stateKey holds the whole shift set for one building. One key rather than one
// per pip: capacity is a property of the set, so counting has to see all of it
// at once, and the actor runtime already serialises access.
const stateKey = "shifts"

// --- state -------------------------------------------------------------------

// daprStore keeps one building's shifts in the Dapr actor state store.
//
// It is only usable from inside an actor invocation. Dapr answers the state API
// with ERR_ACTOR_INSTANCE_MISSING anywhere else, because activation is what
// makes this process authoritative for the id — which is precisely the property
// being bought, and why the Connect handlers forward instead of calling here.
//
// No Lua, and no compare-and-set. The Redis store needs an atomic
// reap-check-claim because several replicas share one key; here the actor
// runtime guarantees one invocation at a time per building, so a plain
// read-modify-write is already indivisible.
type daprStore struct {
	client *http.Client
	base   string
	id     uint64

	max    int
	lease  time.Duration
	maxGap uint64
	now    func() time.Time
}

// NewDaprStore builds a store backed by the actor state store behind the
// sidecar at base (e.g. "http://localhost:3500").
func NewDaprStore(base string, workplaceID uint64, max int, lease time.Duration, maxGap uint64) Store {
	return &daprStore{
		client: &http.Client{Timeout: 5 * time.Second},
		base:   strings.TrimSuffix(base, "/"),
		id:     workplaceID,
		max:    max,
		lease:  lease,
		maxGap: maxGap,
		now:    time.Now,
	}
}

// wireShift is the persisted form. shiftSet's fields are unexported on purpose —
// the rules own them — so the wire shape is written out here rather than by
// tagging the domain type and letting the store's format leak into it.
type wireShift struct {
	StartedTick  uint64    `json:"started_tick"`
	LastWorkTick uint64    `json:"last_work_tick"`
	LastWork     time.Time `json:"last_work"`
}

func (d *daprStore) stateURL(suffix string) string {
	return fmt.Sprintf("%s/v1.0/actors/%s/%d/state%s", d.base, ActorType, d.id, suffix)
}

func (d *daprStore) load(ctx context.Context) (*shiftSet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.stateURL("/"+stateKey), nil)
	if err != nil {
		return nil, err
	}
	res, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	set := newShiftSet(d.max, d.lease, d.maxGap, d.now)

	// A miss is an empty building, not an error: an actor exists as soon as it
	// is addressed, so "never written" and "nobody working here" are the same
	// state and the caller must not be able to tell them apart.
	if res.StatusCode == http.StatusNoContent || res.StatusCode == http.StatusNotFound {
		return set, nil
	}
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("actor state get: %d %s", res.StatusCode, body)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return set, nil
	}

	var wire map[uint64]wireShift
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("actor state decode: %w", err)
	}
	for pip, w := range wire {
		set.shifts[pip] = &shift{
			startedTick:  w.StartedTick,
			lastWorkTick: w.LastWorkTick,
			lastWork:     w.LastWork,
		}
	}
	return set, nil
}

func (d *daprStore) save(ctx context.Context, set *shiftSet) error {
	wire := make(map[uint64]wireShift, len(set.shifts))
	for pip, sh := range set.shifts {
		wire[pip] = wireShift{
			StartedTick:  sh.startedTick,
			LastWorkTick: sh.lastWorkTick,
			LastWork:     sh.lastWork,
		}
	}

	body, err := json.Marshal([]map[string]any{{
		"operation": "upsert",
		"request":   map[string]any{"key": stateKey, "value": wire},
	}})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.stateURL(""), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(res.Body)
		return fmt.Errorf("actor state post: %d %s", res.StatusCode, msg)
	}
	return nil
}

func (d *daprStore) Claim(ctx context.Context, pip, tick uint64) (bool, string, error) {
	set, err := d.load(ctx)
	if err != nil {
		return false, "", err
	}
	accepted, reason := set.claim(pip, tick)
	if !accepted {
		// Nothing changed except possibly a reap, and writing that back on every
		// rejected offer would turn a busy building into a write storm.
		return false, reason, nil
	}
	if err := d.save(ctx, set); err != nil {
		return false, "", err
	}
	return true, "", nil
}

func (d *daprStore) Touch(ctx context.Context, pip, tick uint64) (int32, bool, error) {
	set, err := d.load(ctx)
	if err != nil {
		return 0, false, err
	}
	elapsed, ok := set.touch(pip, tick)
	if !ok {
		return 0, false, nil
	}
	// Written even when elapsed is zero: Touch is what renews the lease, and
	// skipping the write would let a shift expire while it is being worked.
	if err := d.save(ctx, set); err != nil {
		return 0, false, err
	}
	return elapsed, true, nil
}

func (d *daprStore) Release(ctx context.Context, pip, _ uint64) (uint64, bool, error) {
	set, err := d.load(ctx)
	if err != nil {
		return 0, false, err
	}
	started, found := set.release(pip)
	if !found {
		return 0, false, nil
	}
	if err := d.save(ctx, set); err != nil {
		return 0, false, err
	}
	return started, true, nil
}

func (d *daprStore) Count(ctx context.Context) (int, error) {
	set, err := d.load(ctx)
	if err != nil {
		return 0, err
	}
	return set.count(), nil
}

// --- the two halves of a Dapr-hosted workplace -------------------------------

// ActorHost serves pips.workplace.v1 by handing every call to a building's
// actor, and serves the actor endpoints that answer on the other side.
//
// Both halves live in this process and talk through the sidecar. That loop
// looks wasteful and is not optional: state may only be touched inside an
// invocation the sidecar routed, so the trip out and back is what earns the
// right to write. What it buys is one invocation at a time per building —
// the atomicity store.go's Lua exists to provide — plus placement, so which
// replica owns which building stops being this service's problem.
//
// Costs roughly 1.6 ms per call, measured. Irrelevant at the economy's 1 Hz;
// it would not be if anything called a workplace per tick.
type ActorHost struct {
	inner  *Host
	client *http.Client
	base   string
}

func NewActorHost(inner *Host, base string) *ActorHost {
	return &ActorHost{
		inner:  inner,
		client: &http.Client{Timeout: 10 * time.Second},
		base:   strings.TrimSuffix(base, "/"),
	}
}

// call routes one RPC through the sidecar to the building's actor.
//
// The body is the protobuf request as JSON and the reply is the protobuf
// response as JSON, so the actor endpoint stays a bridge rather than a second
// hand-written encoding of the contract that could disagree with the first.
func (a *ActorHost) call(ctx context.Context, id uint64, method string, in, out proto.Message) error {
	body, err := protojson.Marshal(in)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/v1.0/actors/%s/%d/method/%s", a.base, ActorType, id, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return fmt.Errorf("actor %s/%d %s: %d %s", ActorType, id, method, res.StatusCode, raw)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return protojson.Unmarshal(raw, out)
}

// resolveID fills in the building an RPC is for, applying the same
// single-building courtesy Host does so a Dapr-hosted farm behaves identically
// to a plain one.
func (a *ActorHost) resolveID(id uint64) (uint64, error) {
	b, err := a.inner.resolve(id)
	if err != nil {
		return 0, err
	}
	return b.id, nil
}

func (a *ActorHost) List(
	ctx context.Context,
	req *connect.Request[workplacev1.ListRequest],
) (*connect.Response[workplacev1.ListResponse], error) {
	// Enumeration is configuration, not state, so it is answered locally. Only
	// the occupancy inside each Describe needs an actor, and Describe below is
	// what fetches it.
	ids := a.inner.ids()
	out := make([]*workplacev1.DescribeResponse, 0, len(ids))
	for _, id := range ids {
		res, err := a.Describe(ctx, connect.NewRequest(&workplacev1.DescribeRequest{
			WorkplaceId: id,
		}))
		if err != nil {
			return nil, err
		}
		out = append(out, res.Msg)
	}
	_ = req
	return connect.NewResponse(&workplacev1.ListResponse{Workplaces: out}), nil
}

func (a *ActorHost) Describe(
	ctx context.Context,
	req *connect.Request[workplacev1.DescribeRequest],
) (*connect.Response[workplacev1.DescribeResponse], error) {
	id, err := a.resolveID(req.Msg.GetWorkplaceId())
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	msg.WorkplaceId = id

	out := &workplacev1.DescribeResponse{}
	if err := a.call(ctx, id, "Describe", msg, out); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(out), nil
}

func (a *ActorHost) CanEmploy(
	ctx context.Context,
	req *connect.Request[workplacev1.CanEmployRequest],
) (*connect.Response[workplacev1.CanEmployResponse], error) {
	id, err := a.resolveID(req.Msg.GetWorkplaceId())
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	msg.WorkplaceId = id

	out := &workplacev1.CanEmployResponse{}
	if err := a.call(ctx, id, "CanEmploy", msg, out); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(out), nil
}

func (a *ActorHost) StartShift(
	ctx context.Context,
	req *connect.Request[workplacev1.StartShiftRequest],
) (*connect.Response[workplacev1.StartShiftResponse], error) {
	id, err := a.resolveID(req.Msg.GetWorkplaceId())
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	msg.WorkplaceId = id

	out := &workplacev1.StartShiftResponse{}
	if err := a.call(ctx, id, "StartShift", msg, out); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(out), nil
}

func (a *ActorHost) Work(
	ctx context.Context,
	req *connect.Request[workplacev1.WorkRequest],
) (*connect.Response[workplacev1.WorkResponse], error) {
	id, err := a.resolveID(req.Msg.GetWorkplaceId())
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	msg.WorkplaceId = id

	out := &workplacev1.WorkResponse{}
	if err := a.call(ctx, id, "Work", msg, out); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(out), nil
}

func (a *ActorHost) EndShift(
	ctx context.Context,
	req *connect.Request[workplacev1.EndShiftRequest],
) (*connect.Response[workplacev1.EndShiftResponse], error) {
	id, err := a.resolveID(req.Msg.GetWorkplaceId())
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	msg.WorkplaceId = id

	out := &workplacev1.EndShiftResponse{}
	if err := a.call(ctx, id, "EndShift", msg, out); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(out), nil
}

func (a *ActorHost) Buy(
	ctx context.Context,
	req *connect.Request[workplacev1.BuyRequest],
) (*connect.Response[workplacev1.BuyResponse], error) {
	id, err := a.resolveID(req.Msg.GetWorkplaceId())
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	msg.WorkplaceId = id

	out := &workplacev1.BuyResponse{}
	if err := a.call(ctx, id, "Buy", msg, out); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(out), nil
}

// ConsiderOffer answers a queued offer. It goes through an actor like anything
// else that touches state — the offer arrives on a queue rather than an RPC,
// but the write it causes is the same write.
func (a *ActorHost) ConsiderOffer(ctx context.Context, pipID, tick uint64) (bool, string) {
	for _, id := range a.inner.ids() {
		out := &workplacev1.StartShiftResponse{}
		err := a.call(ctx, id, "StartShift", &workplacev1.StartShiftRequest{
			WorkplaceId: id, PipId: pipID, Tick: tick,
		}, out)
		if err != nil {
			slog.Warn("could not offer to a building", "workplace", id, "pip", pipID, "err", err)
			continue
		}
		if out.GetAccepted() {
			slog.Info("offer accepted", "workplace", id, "pip", pipID, "tick", tick)
			return true, ""
		}
	}
	return false, "no free positions"
}

// Workers counts through the actors, not through the buildings.
//
// Delegating to the inner Host would read the state store directly, and outside
// an invocation Dapr refuses that with ERR_ACTOR_INSTANCE_MISSING — so /healthz
// would report a failure on a service that is perfectly healthy. Everything
// that touches state goes the long way round; there is no shortcut for the
// health endpoint either.
func (a *ActorHost) Workers() int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	total := 0
	for _, id := range a.inner.ids() {
		out := &workplacev1.DescribeResponse{}
		if err := a.call(ctx, id, "Describe",
			&workplacev1.DescribeRequest{WorkplaceId: id}, out); err != nil {
			slog.Warn("could not count workers", "workplace", id, "err", err)
			continue
		}
		total += int(out.GetCurrentWorkers())
	}
	return total
}

// --- what Dapr calls back into -----------------------------------------------

// Handler serves the endpoints the sidecar requires: the entity declaration it
// polls at startup, and the invocations it routes to activated actors.
//
// Mounted on the same mux as the Connect handler. They cannot collide: Connect
// owns /pips.workplace.v1.…, these own /dapr/ and /actors/.
func (h *Host) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/dapr/config", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"entities": []string{ActorType},
			// An hour, because a building is not idle in the sense Dapr means:
			// it exists whether or not anyone is working in it, and
			// deactivating one only costs a reload on the next call.
			"actorIdleTimeout":  "1h",
			"actorScanInterval": "30s",
			// Long enough for a Work call to finish rather than being cut in
			// half during a rebalance.
			"drainOngoingCallTimeout": "30s",
			"drainRebalancedActors":   true,
		})
	})

	mux.HandleFunc("/actors/", h.serveActor)
	return mux
}

// serveActor dispatches one actor invocation to the building it names.
//
// This is where business logic legally runs: the sidecar routed the call, so
// the state API will accept writes for this id.
func (h *Host) serveActor(w http.ResponseWriter, r *http.Request) {
	// /actors/<type>/<id>[/method/<name>]
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		http.Error(w, "bad actor path", http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil {
		http.Error(w, "bad actor id", http.StatusBadRequest)
		return
	}

	// Deactivation, and the activation probe Dapr sends with no method. Neither
	// needs anything done: a building keeps its state in the store, so there is
	// nothing in memory to build up or tear down.
	if r.Method == http.MethodDelete || len(parts) < 5 {
		w.WriteHeader(http.StatusOK)
		return
	}

	b, err := h.resolve(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	body, _ := io.ReadAll(r.Body)
	reply, err := dispatch(r.Context(), b, parts[4], body)
	if err != nil {
		// 500 rather than a Connect code: the caller here is the sidecar, and
		// the adapter on the other side turns this into Unavailable.
		slog.Warn("actor invocation failed",
			"workplace", id, "method", parts[4], "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(reply)
}

// dispatch runs one RPC against a building and returns its response as JSON.
func dispatch(ctx context.Context, b *Service, method string, body []byte) ([]byte, error) {
	unmarshal := func(m proto.Message) error {
		if len(bytes.TrimSpace(body)) == 0 {
			return nil
		}
		return protojson.Unmarshal(body, m)
	}

	switch method {
	case "Describe":
		in := &workplacev1.DescribeRequest{}
		if err := unmarshal(in); err != nil {
			return nil, err
		}
		res, err := b.Describe(ctx, connect.NewRequest(in))
		if err != nil {
			return nil, err
		}
		return protojson.Marshal(res.Msg)

	case "CanEmploy":
		in := &workplacev1.CanEmployRequest{}
		if err := unmarshal(in); err != nil {
			return nil, err
		}
		res, err := b.CanEmploy(ctx, connect.NewRequest(in))
		if err != nil {
			return nil, err
		}
		return protojson.Marshal(res.Msg)

	case "StartShift":
		in := &workplacev1.StartShiftRequest{}
		if err := unmarshal(in); err != nil {
			return nil, err
		}
		res, err := b.StartShift(ctx, connect.NewRequest(in))
		if err != nil {
			return nil, err
		}
		return protojson.Marshal(res.Msg)

	case "Work":
		in := &workplacev1.WorkRequest{}
		if err := unmarshal(in); err != nil {
			return nil, err
		}
		res, err := b.Work(ctx, connect.NewRequest(in))
		if err != nil {
			return nil, err
		}
		return protojson.Marshal(res.Msg)

	case "EndShift":
		in := &workplacev1.EndShiftRequest{}
		if err := unmarshal(in); err != nil {
			return nil, err
		}
		res, err := b.EndShift(ctx, connect.NewRequest(in))
		if err != nil {
			return nil, err
		}
		return protojson.Marshal(res.Msg)

	case "Buy":
		in := &workplacev1.BuyRequest{}
		if err := unmarshal(in); err != nil {
			return nil, err
		}
		res, err := b.Buy(ctx, connect.NewRequest(in))
		if err != nil {
			return nil, err
		}
		return protojson.Marshal(res.Msg)

	default:
		return nil, fmt.Errorf("unknown actor method %q", method)
	}
}
