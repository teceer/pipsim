// Command farm is a workplace that produces grain.
//
// It implements pips.workplace.v1.WorkplaceService and nothing else. It does
// not know that other workplaces exist, and it holds no pip state beyond who is
// currently on shift.
package main

import (
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"
)

// shift tracks who is working right now. Deliberately not persisted: pips
// belong to sim-core, and a restart should re-derive shifts from it rather than
// trusting local state.
type shifts struct {
	mu      sync.RWMutex
	workers map[uint64]time.Time
}

const maxWorkers = 4

// grainPerTick is the farm's entire domain rule. It lives here and nowhere
// else — sim-core knows nothing about grain.
const grainPerTick = 3

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	s := &shifts{workers: make(map[uint64]time.Time)}
	_ = s

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// TODO: mount the generated Connect handler for WorkplaceService.
	//   Describe   -> kind "farm", maxWorkers, produces GRAIN
	//   CanEmploy  -> len(workers) < maxWorkers
	//   Work       -> grainPerTick, need_deltas{FOOD: -2}
	//   EndShift   -> when the field is exhausted or night falls

	slog.Info("farm listening", "addr", ":8090", "max_workers", maxWorkers)
	if err := http.ListenAndServe(":8090", mux); err != nil {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
}
