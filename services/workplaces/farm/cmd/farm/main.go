// Command farm is a workplace that grows grain.
//
// It implements pips.workplace.v1.WorkplaceService and nothing else. It does
// not know that other workplaces exist, and it holds no pip state beyond who is
// currently on shift.
package main

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/teceer/pipsim/gen/go/pips/workplace/v1/workplacev1connect"
	"github.com/teceer/pipsim/services/workplaces/farm/internal/farm"
)

func envInt(key string, def int64) int64 {
	if v, err := strconv.ParseInt(os.Getenv(key), 10, 64); err == nil {
		return v
	}
	return def
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	// Position and id are configuration for now. Once BuildWorkplace works,
	// both arrive from the player action that created the building.
	svc := farm.New(
		uint64(envInt("WORKPLACE_ID", 1)),
		int32(envInt("WORKPLACE_X", 12_000)),
		int32(envInt("WORKPLACE_Y", 8_000)),
	)

	mux := http.NewServeMux()
	mux.Handle(workplacev1connect.NewWorkplaceServiceHandler(svc))

	// Part of the operational contract every service in this repo implements,
	// whatever the language.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","workers":` +
			strconv.Itoa(svc.Workers()) + `}`))
	})

	srv := &http.Server{
		Addr:              ":8090",
		Handler:           h2c.NewHandler(mux, &http2.Server{}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	slog.Info("farm listening", "addr", srv.Addr, "max_workers", farm.MaxWorkers)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
}
