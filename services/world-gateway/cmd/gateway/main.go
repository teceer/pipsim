// Command gateway is the only service browsers talk to.
//
// It aggregates sim-core and the workplaces and serves them over Connect-RPC,
// which speaks both to browsers and to other services. That choice is why there
// is no Envoy or gRPC-Web bridge anywhere in this repo.
//
// The gateway holds no domain logic. Every rule about pips lives in sim-core;
// every rule about a building lives in that building's service.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	mux := http.NewServeMux()

	// Liveness. Part of the operational contract every service in this repo
	// implements, whatever the language.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// TODO: mount the generated Connect handler for pips.world.v1.WorldService
	// from gen/go, and dial sim-core over gRPC.

	// h2c so that HTTP/2 works without TLS inside the cluster, while browsers
	// still reach the same handler over HTTP/1.1.
	srv := &http.Server{
		Addr:              ":8081",
		Handler:           h2c.NewHandler(mux, &http2.Server{}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("world-gateway listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("shutdown failed", "err", err)
	}
	slog.Info("world-gateway stopped")
}
