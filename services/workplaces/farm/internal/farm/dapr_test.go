package farm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	workplacev1 "github.com/teceer/pipsim/gen/go/pips/workplace/v1"
)

// fakeSidecar answers the slice of the Dapr HTTP API the store uses, keeping
// state in memory.
//
// Not a mock of the store — the store under test is real, and this stands in
// for the sidecar so the rules can be checked without a container. What it
// deliberately does *not* reproduce is turn-based concurrency: Dapr serialises
// invocations per actor, and a test that pretended otherwise would be asserting
// something the real runtime does not require of this code.
type fakeSidecar struct {
	base  string
	mu    sync.Mutex
	state map[string][]byte
	calls int
}

func newFakeSidecar(t *testing.T) *fakeSidecar {
	t.Helper()
	f := &fakeSidecar{state: map[string][]byte{}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls++

		// /v1.0/actors/<type>/<id>/state[/<key>]
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		actorKey := strings.Join(parts[2:4], "/")

		switch r.Method {
		case http.MethodGet:
			v, ok := f.state[actorKey]
			if !ok {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_, _ = w.Write(v)

		case http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			var ops []struct {
				Operation string          `json:"operation"`
				Request   json.RawMessage `json:"request"`
			}
			if err := json.Unmarshal(body, &ops); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			for _, op := range ops {
				var req struct {
					Key   string          `json:"key"`
					Value json.RawMessage `json:"value"`
				}
				_ = json.Unmarshal(op.Request, &req)
				f.state[actorKey] = req.Value
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)

	f.base = srv.URL
	return f
}

func (f *fakeSidecar) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func daprStoreFor(t *testing.T, f *fakeSidecar, id uint64) Store {
	t.Helper()
	return NewDaprStore(f.base, id, MaxWorkers, ShiftLease, MaxTicksPerWork)
}

func TestDaprStoreRoundTripsAShift(t *testing.T) {
	f := newFakeSidecar(t)
	s := daprStoreFor(t, f, 1)
	ctx := context.Background()

	accepted, _, err := s.Claim(ctx, 7, 1)
	if err != nil || !accepted {
		t.Fatalf("Claim: accepted=%v err=%v", accepted, err)
	}

	n, err := s.Count(ctx)
	if err != nil || n != 1 {
		t.Fatalf("Count: %d, %v", n, err)
	}

	elapsed, ok, err := s.Touch(ctx, 7, 11)
	if err != nil || !ok {
		t.Fatalf("Touch: ok=%v err=%v", ok, err)
	}
	if elapsed != 10 {
		t.Errorf("want 10 elapsed ticks, got %d", elapsed)
	}

	started, found, err := s.Release(ctx, 7, 20)
	if err != nil || !found {
		t.Fatalf("Release: found=%v err=%v", found, err)
	}
	if started != 1 {
		t.Errorf("want the shift to have started on tick 1, got %d", started)
	}

	if n, _ := s.Count(ctx); n != 0 {
		t.Errorf("want an empty building after Release, got %d", n)
	}
}

// The state key carries the building id, so two farms in one process cannot
// see each other's shifts however they are hosted.
func TestDaprStoreKeepsBuildingsApart(t *testing.T) {
	f := newFakeSidecar(t)
	one, three := daprStoreFor(t, f, 1), daprStoreFor(t, f, 3)
	ctx := context.Background()

	if _, _, err := one.Claim(ctx, 7, 1); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	if n, _ := one.Count(ctx); n != 1 {
		t.Errorf("farm 1: want 1 worker, got %d", n)
	}
	if n, _ := three.Count(ctx); n != 0 {
		t.Errorf("farm 3: want 0 workers, got %d", n)
	}
	if _, ok, _ := three.Touch(ctx, 7, 2); ok {
		t.Error("farm 3 paid a pip employed by farm 1")
	}
}

// A building nobody has written to yet is empty, not missing. Dapr answers 204
// for an actor that has never saved state, and telling that apart from "no
// shifts" would make the first Claim of a building's life fail.
func TestDaprStoreTreatsAnUnwrittenBuildingAsEmpty(t *testing.T) {
	f := newFakeSidecar(t)
	s := daprStoreFor(t, f, 99)

	n, err := s.Count(context.Background())
	if err != nil {
		t.Fatalf("Count on a fresh building: %v", err)
	}
	if n != 0 {
		t.Errorf("want 0, got %d", n)
	}
}

// A rejected claim must not write. Offers are rejected constantly once a
// building is full, and persisting the unchanged set on each one would turn a
// busy farm into a write storm against the state store.
func TestDaprStoreDoesNotWriteOnARejectedClaim(t *testing.T) {
	f := newFakeSidecar(t)
	s := daprStoreFor(t, f, 1)
	ctx := context.Background()

	if _, _, err := s.Claim(ctx, 7, 1); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	before := f.callCount()
	accepted, reason, err := s.Claim(ctx, 7, 2) // already on shift
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if accepted {
		t.Fatal("the same pip was claimed twice")
	}
	if reason == "" {
		t.Error("a rejection should say why")
	}
	if got := f.callCount() - before; got != 1 {
		t.Errorf("want one call (the read), got %d — a rejection wrote state", got)
	}
}

// The lease still expires. Reaping lives in shiftSet, shared with memStore, so
// this is really a check that the Dapr store routes through the same rules
// rather than reimplementing them.
func TestDaprStoreReapsAnExpiredLease(t *testing.T) {
	f := newFakeSidecar(t)
	s := NewDaprStore(f.base, 1, MaxWorkers, 50*time.Millisecond, MaxTicksPerWork)
	ctx := context.Background()

	if _, _, err := s.Claim(ctx, 7, 1); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	time.Sleep(80 * time.Millisecond)

	// Someone else claiming is what triggers the reap.
	if accepted, _, err := s.Claim(ctx, 8, 2); err != nil || !accepted {
		t.Fatalf("Claim after expiry: accepted=%v err=%v", accepted, err)
	}
	if n, _ := s.Count(ctx); n != 1 {
		t.Errorf("want only the new shift, got %d", n)
	}
}

// dispatch is the actor endpoint's half of the bridge. It must speak the same
// contract as the Connect handler, because the whole point of encoding the
// bodies as protojson is that there is one shape rather than two.
func TestDispatchRunsTheContractAgainstABuilding(t *testing.T) {
	b := New(1)
	ctx := context.Background()

	raw, err := dispatch(ctx, b, "StartShift",
		[]byte(`{"workplaceId":"1","pipId":"7","tick":"1"}`))
	if err != nil {
		t.Fatalf("dispatch StartShift: %v", err)
	}
	if !strings.Contains(string(raw), "accepted") {
		t.Errorf("StartShift replied %s", raw)
	}

	raw, err = dispatch(ctx, b, "Describe", []byte(`{"workplaceId":"1"}`))
	if err != nil {
		t.Fatalf("dispatch Describe: %v", err)
	}
	if !strings.Contains(string(raw), `"currentWorkers":1`) {
		t.Errorf("Describe did not see the shift: %s", raw)
	}
}

func TestDispatchRejectsAnUnknownMethod(t *testing.T) {
	if _, err := dispatch(context.Background(), New(1), "Nope", nil); err == nil {
		t.Fatal("want an error for a method that is not in the contract")
	}
}

// An empty body is what Dapr sends for a method with no arguments, and
// protojson refuses to unmarshal nothing.
func TestDispatchAcceptsAnEmptyBody(t *testing.T) {
	if _, err := dispatch(context.Background(), New(1), "Describe", nil); err != nil {
		t.Fatalf("dispatch with no body: %v", err)
	}
}

// The entity declaration is how the sidecar learns this app hosts farms. Get it
// wrong and actors are never registered, which fails as "actor instance is
// missing" a long way from the cause.
func TestHandlerDeclaresTheEntity(t *testing.T) {
	srv := httptest.NewServer(NewHost(New(1)).Handler())
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/dapr/config")
	if err != nil {
		t.Fatalf("GET /dapr/config: %v", err)
	}
	defer res.Body.Close()

	var cfg struct {
		Entities []string `json:"entities"`
	}
	if err := json.NewDecoder(res.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(cfg.Entities) != 1 || cfg.Entities[0] != ActorType {
		t.Errorf("want entities [%s], got %v", ActorType, cfg.Entities)
	}
}

func TestActorEndpointRoutesToTheNamedBuilding(t *testing.T) {
	host := NewHost(New(1), New(3))
	srv := httptest.NewServer(host.Handler())
	t.Cleanup(srv.Close)

	post := func(path, body string) *http.Response {
		t.Helper()
		res, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		return res
	}

	res := post("/actors/farm/3/method/StartShift", `{"workplaceId":"3","pipId":"7","tick":"1"}`)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("StartShift: %d", res.StatusCode)
	}

	one, _ := host.Describe(context.Background(), connect.NewRequest(
		&workplacev1.DescribeRequest{WorkplaceId: 1}))
	three, _ := host.Describe(context.Background(), connect.NewRequest(
		&workplacev1.DescribeRequest{WorkplaceId: 3}))

	if one.Msg.GetCurrentWorkers() != 0 {
		t.Errorf("farm 1 should be untouched, has %d", one.Msg.GetCurrentWorkers())
	}
	if three.Msg.GetCurrentWorkers() != 1 {
		t.Errorf("farm 3 should hold the shift, has %d", three.Msg.GetCurrentWorkers())
	}
}

func TestActorEndpointIsNotFoundForAnUnhostedBuilding(t *testing.T) {
	srv := httptest.NewServer(NewHost(New(1)).Handler())
	t.Cleanup(srv.Close)

	res, err := http.Post(srv.URL+"/actors/farm/404/method/Describe",
		"application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("want 404 for a building this host does not have, got %d", res.StatusCode)
	}
}

// Activation and deactivation carry no method and must simply succeed. A
// building keeps its state in the store, so there is nothing to build up or
// tear down.
func TestActorLifecycleProbesSucceed(t *testing.T) {
	srv := httptest.NewServer(NewHost(New(1)).Handler())
	t.Cleanup(srv.Close)

	res, err := http.Post(srv.URL+"/actors/farm/1", "application/json", nil)
	if err != nil {
		t.Fatalf("activation probe: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("activation: want 200, got %d", res.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/actors/farm/1", nil)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("deactivation: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("deactivation: want 200, got %d", res.StatusCode)
	}
}
