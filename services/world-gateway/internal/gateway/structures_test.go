package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	simv1 "github.com/teceer/pipsim/gen/go/pips/sim/v1"
)

func TestParseStructures(t *testing.T) {
	got := ParseStructures(
		"bank|double-entry ledger|http://bank:8082/healthz; sim-core|the world itself|")

	if len(got) != 2 {
		t.Fatalf("want 2 structures, got %d: %+v", len(got), got)
	}
	if got[0].Kind != "bank" || got[0].Role != "double-entry ledger" {
		t.Errorf("bank parsed as %+v", got[0])
	}
	if got[0].HealthURL != "http://bank:8082/healthz" {
		t.Errorf("health url parsed as %q", got[0].HealthURL)
	}
	// A trailing empty field is how you say "nothing to poll".
	if got[1].Kind != "sim-core" || got[1].HealthURL != "" {
		t.Errorf("sim-core parsed as %+v", got[1])
	}
}

// An empty variable disables the feature rather than producing one nameless
// building, which is what a naive Split would give.
func TestParseStructuresIgnoresEmptyEntries(t *testing.T) {
	for _, raw := range []string{"", "  ", ";;", "|role|url"} {
		if got := ParseStructures(raw); len(got) != 0 {
			t.Errorf("ParseStructures(%q) = %+v, want none", raw, got)
		}
	}
}

type recordingSim struct {
	intents []*simv1.RegisterStructureIntent
}

func (r *recordingSim) SubmitIntent(
	_ context.Context,
	req *connect.Request[simv1.SubmitIntentRequest],
) (*connect.Response[simv1.SubmitIntentResponse], error) {
	if s := req.Msg.GetRegisterStructure(); s != nil {
		r.intents = append(r.intents, s)
	}
	return connect.NewResponse(&simv1.SubmitIntentResponse{Accepted: true}), nil
}

// The whole point of the health poll: a service that does not answer is drawn
// dark, and one that does is drawn lit.
func TestRegisterAllReportsHealth(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer down.Close()

	sim := &recordingSim{}
	r := NewStructureRegistrar(sim, []Structure{
		{Kind: "bank", Role: "ledger", HealthURL: up.URL},
		{Kind: "broadcast", Role: "channels", HealthURL: down.URL},
		{Kind: "sim-core", Role: "the world"},
	})
	r.registerAll(context.Background())

	if len(sim.intents) != 3 {
		t.Fatalf("want 3 intents, got %d", len(sim.intents))
	}
	if !sim.intents[0].GetHealthy() {
		t.Error("a service answering 200 was reported unhealthy")
	}
	if sim.intents[1].GetHealthy() {
		t.Error("a service answering 503 was reported healthy")
	}
	// No URL means nothing to poll, which is not the same as being down.
	if !sim.intents[2].GetHealthy() {
		t.Error("a structure with no health URL should be assumed up")
	}
}

// Ids come from position in the list, so a restarted gateway updates the same
// buildings instead of adding a second row of them.
func TestStructureIDsAreStableAcrossRuns(t *testing.T) {
	structures := []Structure{{Kind: "bank"}, {Kind: "broadcast"}}

	first := &recordingSim{}
	NewStructureRegistrar(first, structures).registerAll(context.Background())
	second := &recordingSim{}
	NewStructureRegistrar(second, structures).registerAll(context.Background())

	for i := range first.intents {
		if first.intents[i].GetStructureId() != second.intents[i].GetStructureId() {
			t.Errorf("%s changed id between runs: %d then %d",
				first.intents[i].GetKind(),
				first.intents[i].GetStructureId(),
				second.intents[i].GetStructureId())
		}
	}
	if first.intents[0].GetStructureId() == first.intents[1].GetStructureId() {
		t.Error("two structures share an id")
	}
}
