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
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/teceer/pipsim/gen/go/pips/bank/v1/bankv1connect"
	"github.com/teceer/pipsim/gen/go/pips/sim/v1/simv1connect"
	"github.com/teceer/pipsim/gen/go/pips/workplace/v1/workplacev1connect"
	"github.com/teceer/pipsim/gen/go/pips/world/v1/worldv1connect"
	"github.com/teceer/pipsim/services/shared/gotel"
	"github.com/teceer/pipsim/services/world-gateway/internal/economy"
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

			// Health-check idle connections, because in a cluster the peer
			// disappears without closing anything.
			//
			// This cost a stalled gateway: rolling the farm terminated every pod
			// it was connected to, and the transport went on writing requests
			// into a connection nobody was reading. No error, no timeout, no log
			// — the economy driver simply blocked forever inside one Work call.
			// A pod is not a server that says goodbye.
			ReadIdleTimeout: 10 * time.Second,
			PingTimeout:     5 * time.Second,

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

// workplaceAddrs reads the list of workplaces to drive.
//
// Addresses only. Which id and kind lives at each one is answered by the
// workplace itself through `Describe`, so a Helm value cannot disagree with a
// service's own configuration about who it is.
func workplaceAddrs() []string {
	var out []string
	for _, a := range strings.Split(os.Getenv("WORKPLACE_ADDRS"), ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}

	// Accepted for one release so a chart still carrying the single-farm
	// variable keeps its economy running instead of silently losing it.
	if farm := strings.TrimSpace(os.Getenv("FARM_ADDR")); farm != "" {
		slog.Warn("FARM_ADDR is deprecated; use WORKPLACE_ADDRS", "addr", farm)
		out = append(out, farm)
	}
	return out
}

func main() {
	ctx := context.Background()
	shutdown, err := gotel.Init(ctx, "world-gateway")
	if err != nil {
		slog.Error("could not init telemetry", "err", err)
		os.Exit(1)
	}
	defer shutdown(ctx)

	otelInterceptor, err := gotel.Interceptor()
	if err != nil {
		slog.Error("could not build otel interceptor", "err", err)
		os.Exit(1)
	}

	simAddr := env("SIM_CORE_ADDR", "localhost:50051")
	seed := envUint("SIM_SEED", 42)
	tickHz := envUint("SIM_TICK_HZ", 10)

	// connect.WithGRPC because sim-core is tonic, which speaks only gRPC.
	// The same generated client would talk Connect to a Connect server —
	// one contract, both protocols. gotel.WithInterceptor carries the trace
	// across this hop, which is the one that used to drop it entirely.
	simClient := simv1connect.NewSimServiceClient(
		h2cClient(),
		"http://"+simAddr,
		connect.WithGRPC(),
		gotel.WithInterceptor(otelInterceptor),
	)

	svc := gateway.New(simClient, seed, int32(tickHz))

	// The economy loop: fill each workplace's positions and forward what a shift
	// does to the workers. Mechanics only — see internal/economy for why the
	// policy deliberately lives elsewhere.
	amqpURL := os.Getenv("AMQP_URL")
	addrs := workplaceAddrs()
	if len(addrs) > 0 && amqpURL != "" {
		offers, err := economy.Dial(amqpURL)
		if err != nil {
			slog.Error("could not reach the broker", "err", err)
			os.Exit(1)
		}
		defer offers.Close()

		// One driver per building, not per address: a workplace service owns a
		// kind of building and may host several.
		//
		// Discovery happens once, here. That is a real limit — a building added
		// to a running service is invisible until this restarts — and it is
		// deliberate for now, because ids are still configuration. It stops
		// being acceptable the moment BuildWorkplace exists.
		discoverCtx, cancelDiscover := context.WithTimeout(context.Background(), 15*time.Second)
		drivers := make([]*economy.Driver, 0, len(addrs))
		for _, addr := range addrs {
			found, err := economy.Discover(
				discoverCtx,
				simClient,
				workplacev1connect.NewWorkplaceServiceClient(h2cClient(), "http://"+addr,
					gotel.WithInterceptor(otelInterceptor)),
				addr,
				offers,
			)
			if err != nil {
				// Not fatal: a workplace that is down at startup should not stop
				// the gateway serving the world, and KeepRegistered would have
				// retried anyway had we known what to retry.
				slog.Warn("could not discover a workplace", "addr", addr, "err", err)
				continue
			}
			slog.Info("workplaces discovered", "addr", addr, "buildings", len(found))
			drivers = append(drivers, found...)
		}
		cancelDiscover()

		if len(drivers) == 0 {
			slog.Error("no workplaces reachable; economy disabled")
		}

		// A gateway with no bank reachable still runs the economy — it just
		// never pays wages or lets a pip buy anything. Wages resume as soon
		// as BANK_ADDR is reachable, so this is a degrade, not a hard
		// dependency.
		var bankClient bankv1connect.BankServiceClient
		if bankAddr := env("BANK_ADDR", ""); bankAddr != "" {
			// Plain Connect, like the workplace clients below: bank is a Go
			// Connect service, not tonic-only gRPC like sim-core.
			bankClient = bankv1connect.NewBankServiceClient(h2cClient(), "http://"+bankAddr)
		} else {
			slog.Info("BANK_ADDR not set; wages and purchases disabled")
		}

		fleet := economy.NewFleet(simClient, bankClient, drivers...)

		ctx, cancelEconomy := context.WithCancel(context.Background())
		defer cancelEconomy()

		fleet.KeepRegistered(ctx, 10*time.Second)
		go fleet.Run(ctx, time.Second)
		go economy.RunOutcomes(ctx, amqpURL, fleet.OnHired)

		svc = svc.WithWorkplaces(fleet.Describe).WithBuy(fleet.Buy)

		if _, err := otel.Meter("world-gateway").Int64ObservableGauge(
			"pipsim.economy.offers_pending",
			otelmetric.WithDescription("Offers waiting in the work queue, per workplace kind"),
			otelmetric.WithInt64Callback(func(_ context.Context, o otelmetric.Int64Observer) error {
				seen := make(map[string]bool, len(fleet.Drivers()))
				for _, d := range fleet.Drivers() {
					kind := d.Kind()
					if kind == "" || seen[kind] {
						continue
					}
					seen[kind] = true
					depth, err := offers.QueueDepth(kind)
					if err != nil {
						slog.Warn("could not read queue depth", "kind", kind, "err", err)
						continue
					}
					o.Observe(int64(depth), otelmetric.WithAttributes(
						attribute.String("building_type", kind),
					))
				}
				return nil
			}),
		); err != nil {
			slog.Error("could not register offers_pending metric", "err", err)
			os.Exit(1)
		}

		slog.Info("economy started", "workplaces", addrs)
	} else {
		slog.Info("economy disabled; WORKPLACE_ADDRS and AMQP_URL are both required")
	}

	mux := http.NewServeMux()
	mux.Handle(worldv1connect.NewWorldServiceHandler(svc, gotel.WithInterceptor(otelInterceptor)))

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
