// Services on the map.
//
// Every microservice gets a building, so the map doubles as a diagram of what
// is deployed rather than only of what the pips are doing. The gateway does
// this because it is already the only service that knows where the others live
// — a bank needs to implement nothing to appear. See ADR 0011.
//
// This is presentation, not domain logic: the gateway decides where the
// buildings stand and whether they are lit, and holds no rule about what they
// mean. sim-core keeps them, the same as it keeps workplaces.

package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	simv1 "github.com/teceer/pipsim/gen/go/pips/sim/v1"
)

const (
	// Milli-tiles, matching sim-core's fixed point. Structures stand in a row
	// along the top edge, clear of the workplaces the economy places.
	structureRowY     = 1_500
	structureSpacingX = 4_000
	structureFirstX   = 2_000

	// Ids are assigned from this base by position in the configured list, so a
	// restart re-registers the same building rather than growing a second one.
	// Far above any workplace id, which come from the workplace services.
	structureIDBase = 900_000

	healthTimeout  = 2 * time.Second
	healthInterval = 5 * time.Second
)

// Structure is one service to draw.
type Structure struct {
	Kind string
	Role string
	// Health endpoint. Empty means "assume up" — for a service that has no
	// /healthz to poll, or is the gateway itself.
	HealthURL string
}

// ParseStructures reads the STRUCTURES variable.
//
// Format: `kind|role|health-url`, entries separated by `;`. The health URL may
// be omitted:
//
//	bank|double-entry ledger|http://bank:8082/healthz;sim-core|the world itself|
//
// Position is not configurable. Where a service stands says nothing about the
// architecture, and a coordinate per service in every environment file is
// upkeep with no reader — they are laid out in a row, in the order given.
func ParseStructures(raw string) []Structure {
	var out []Structure
	for _, entry := range strings.Split(raw, ";") {
		if entry = strings.TrimSpace(entry); entry == "" {
			continue
		}
		parts := strings.Split(entry, "|")
		s := Structure{Kind: strings.TrimSpace(parts[0])}
		if s.Kind == "" {
			continue
		}
		if len(parts) > 1 {
			s.Role = strings.TrimSpace(parts[1])
		}
		if len(parts) > 2 {
			s.HealthURL = strings.TrimSpace(parts[2])
		}
		out = append(out, s)
	}
	return out
}

// StructureRegistrar keeps the structures registered and their health current.
type StructureRegistrar struct {
	sim        SimClient
	structures []Structure
	client     *http.Client
}

// SimClient is the slice of the sim-core client this needs. Declared here
// rather than imported whole so the registrar can be tested without one.
type SimClient interface {
	SubmitIntent(context.Context, *connect.Request[simv1.SubmitIntentRequest]) (*connect.Response[simv1.SubmitIntentResponse], error)
}

func NewStructureRegistrar(sim SimClient, structures []Structure) *StructureRegistrar {
	return &StructureRegistrar{
		sim:        sim,
		structures: structures,
		client:     &http.Client{Timeout: healthTimeout},
	}
}

// Run registers every structure and keeps re-registering as health changes.
//
// Re-registering unconditionally rather than on change: the intent is
// idempotent, sim-core may have restarted with an empty world, and five
// messages every five seconds is not a cost worth optimising against a
// restarted core that silently stays empty.
func (r *StructureRegistrar) Run(ctx context.Context) {
	if len(r.structures) == 0 {
		slog.Info("no STRUCTURES configured; the map shows workplaces only")
		return
	}

	slog.Info("putting services on the map", "count", len(r.structures))

	ticker := time.NewTicker(healthInterval)
	defer ticker.Stop()
	for {
		r.registerAll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *StructureRegistrar) registerAll(ctx context.Context) {
	for i, s := range r.structures {
		healthy := r.healthy(ctx, s)
		if _, err := r.sim.SubmitIntent(ctx, connect.NewRequest(&simv1.SubmitIntentRequest{
			Intent: &simv1.SubmitIntentRequest_RegisterStructure{
				RegisterStructure: &simv1.RegisterStructureIntent{
					StructureId: uint64(structureIDBase + i),
					Kind:        s.Kind,
					Position: &simv1.Vec2{
						XMilli: int32(structureFirstX + i*structureSpacingX),
						YMilli: structureRowY,
					},
					Role:    s.Role,
					Healthy: healthy,
				},
			},
		})); err != nil {
			slog.Warn("could not put a service on the map", "kind", s.Kind, "err", err)
		}
	}
}

// healthy reports whether the service answered. A structure with no health URL
// is drawn as up: it is on the map because someone configured it, and inventing
// a failure for a service nobody can poll would be a worse lie than assuming it
// works.
func (r *StructureRegistrar) healthy(ctx context.Context, s Structure) bool {
	if s.HealthURL == "" {
		return true
	}

	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.HealthURL, nil)
	if err != nil {
		return false
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
