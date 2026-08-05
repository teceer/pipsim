// Command gateway is the only service browsers talk to.
//
// It serves pips.world.v1.WorldService over Connect, which speaks both to
// browsers and to other services from the same .proto. That choice is why there
// is no Envoy or gRPC-Web bridge anywhere in this repo.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/teceer/pipsim/gen/go/pips/sim/v1/simv1connect"
	"github.com/teceer/pipsim/gen/go/pips/world/v1/worldv1connect"
	"github.com/teceer/pipsim/services/world-gateway/internal/gateway"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envUint(key string, def uint64) uint64 {
	if v, err := strconv.ParseUint(os.Getenv(key), 10, 64); err == nil {
		return v
	}
	return def
}

// h2cClient dials sim-core, which serves plaintext HTTP/2.
//
// Go's default transport will not negotiate HTTP/2 without TLS, and gRPC
// requires HTTP/2 — so without this the client silently falls back to HTTP/1.1
// and every call fails with a protocol error.
func h2cClient() *http.Client {
	return &http.Client{
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			},
		},
	}
}

// withCORS lets the browser client call this from a different origin.
//
// Connect sends a few non-standard headers, and a preflight that does not allow
// them fails in a way that looks like the server is down. The permissive origin
// is fine for a local dev cluster and would not be for anything else.
func withCORS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers",
			"Content-Type, Connect-Protocol-Version, Connect-Timeout-Ms, "+
				"Grpc-Timeout, X-Grpc-Web, X-User-Agent")
		w.Header().Set("Access-Control-Expose-Headers",
			"Grpc-Status, Grpc-Message, Connect-Content-Encoding")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	simAddr := env("SIM_CORE_ADDR", "localhost:50051")
	seed := envUint("SIM_SEED", 42)
	tickHz := envUint("SIM_TICK_HZ", 10)

	// connect.WithGRPC because sim-core is tonic, which speaks only gRPC.
	// The same generated client would talk Connect to a Connect server —
	// one contract, both protocols.
	simClient := simv1connect.NewSimServiceClient(
		h2cClient(),
		"http://"+simAddr,
		connect.WithGRPC(),
	)

	svc := gateway.New(simClient, seed, int32(tickHz))

	mux := http.NewServeMux()
	mux.Handle(worldv1connect.NewWorldServiceHandler(svc))

	// Part of the operational contract every service in this repo implements,
	// whatever the language.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// h2c so HTTP/2 works without TLS inside the cluster, while browsers still
	// reach the same handler over HTTP/1.1.
	srv := &http.Server{
		Addr:              ":8081",
		Handler:           h2c.NewHandler(withCORS(mux), &http2.Server{}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("world-gateway listening", "addr", srv.Addr, "sim_core", simAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	// Generous: a server stream in flight should be allowed to finish rather
	// than being cut mid-delta.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("shutdown failed", "err", err)
	}
	slog.Info("world-gateway stopped")
}
