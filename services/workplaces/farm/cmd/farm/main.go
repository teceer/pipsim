// Command farm is a workplace that grows grain.
//
// It implements pips.workplace.v1.WorkplaceService and nothing else. It does
// not know that other workplaces exist, and it holds no pip state beyond who is
// currently on shift.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/teceer/pipsim/gen/go/pips/workplace/v1/workplacev1connect"
	"github.com/teceer/pipsim/services/shared/gotel"
	"github.com/teceer/pipsim/services/workplaces/farm/internal/farm"
	"github.com/teceer/pipsim/services/workplaces/farm/internal/queue"
)

func envInt(key string, def int64) int64 {
	if v, err := strconv.ParseInt(os.Getenv(key), 10, 64); err == nil {
		return v
	}
	return def
}

// workplaceSpecs reads which buildings this process hosts.
//
// WORKPLACES wins when set; otherwise the single-building variables the chart
// still carries are read as a one-entry list, so nothing that worked before
// this change stops working.
func workplaceSpecs() ([]farm.Spec, error) {
	if raw := strings.TrimSpace(os.Getenv("WORKPLACES")); raw != "" {
		return farm.ParseSpecs(raw)
	}
	return []farm.Spec{{
		ID: uint64(envInt("WORKPLACE_ID", 1)),
		X:  int32(envInt("WORKPLACE_X", 12_000)),
		Y:  int32(envInt("WORKPLACE_Y", 8_000)),
	}}, nil
}

// workplaceService is what main needs of a host, whichever half it is talking
// to: the contract itself, plus the two things the queue consumer and the
// health endpoint ask for.
type workplaceService interface {
	workplacev1connect.WorkplaceServiceHandler
	ConsiderOffer(ctx context.Context, pipID, tick uint64) (bool, string, uint64)
	Workers() int
}

// daprSidecar reports where this pod's sidecar is, or "" if there is none.
//
// DAPR_HTTP_PORT is injected by the sidecar itself, so its presence is the
// signal — there is no separate flag to get out of step with reality.
func daprSidecar() string {
	port := strings.TrimSpace(os.Getenv("DAPR_HTTP_PORT"))
	if port == "" {
		return ""
	}
	return "http://localhost:" + port
}

func main() {
	ctx := context.Background()
	shutdown, err := gotel.Init(ctx, "farm")
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

	// This process hosts a *kind* of building, not one building. WORKPLACES is
	// the multi-building form; the single WORKPLACE_ID/X/Y triple still works
	// and means exactly one farm, which is what the Helm chart and every local
	// `make run` still pass.
	//
	// Ids and positions are configuration either way. Once BuildWorkplace
	// exists they arrive from the player action that created the building, and
	// this whole block becomes a bootstrap for an empty world.
	specs, err := workplaceSpecs()
	if err != nil {
		slog.Error("bad workplace configuration", "err", err)
		os.Exit(1)
	}

	// Three places shifts can live, in order of preference.
	//
	// A Dapr sidecar wins: the actor runtime serialises invocations per
	// building, so reap-check-claim is indivisible without the Lua the Redis
	// store needs. Redis is the answer without one — Work and EndShift are
	// load-balanced RPCs, so a pip hired by one replica has to be known to the
	// next. Memory is correct at exactly one replica and is what tests and
	// `make run` use.
	//
	// One store per building, never one shared: capacity is a property of a
	// building, and a store spanning two of them would enforce 24 workers
	// across the pair.
	daprBase := daprSidecar()

	var rdb *redis.Client
	if redisURL := os.Getenv("REDIS_URL"); redisURL != "" && daprBase == "" {
		opts, err := redis.ParseURL(redisURL)
		if err != nil {
			slog.Error("bad REDIS_URL", "err", err)
			os.Exit(1)
		}
		rdb = redis.NewClient(opts)
		slog.Info("shift state in redis", "addr", opts.Addr)
	}
	if daprBase == "" && rdb == nil {
		slog.Info("no sidecar and no REDIS_URL; shift state is per-replica")
	}

	buildings := make([]*farm.Service, 0, len(specs))
	for _, sp := range specs {
		switch {
		case daprBase != "":
			buildings = append(buildings, farm.NewWithStore(
				farm.NewDaprStore(daprBase, sp.ID, farm.MaxWorkers,
					farm.ShiftLease, farm.MaxTicksPerWork),
				sp.ID, sp.X, sp.Y,
			))
		case rdb != nil:
			buildings = append(buildings, farm.NewWithStore(
				farm.NewRedisStore(rdb, sp.ID, farm.MaxWorkers,
					farm.ShiftLease, farm.MaxTicksPerWork),
				sp.ID, sp.X, sp.Y,
			))
		default:
			buildings = append(buildings, farm.New(sp.ID, sp.X, sp.Y))
		}
	}
	host := farm.NewHost(buildings...)

	// Under Dapr the Connect handler is an adapter: it hands each call to the
	// building's actor through the sidecar, and the actor endpoints on the same
	// mux answer on the other side. Without a sidecar the host serves the
	// contract directly, which is what keeps `make test` and `make run`
	// cluster-free.
	var svc workplaceService = host
	if daprBase != "" {
		svc = farm.NewActorHost(host, daprBase)
		slog.Info("shift state in the dapr actor store", "sidecar", daprBase)
	}

	// svc.Workers(), not host.Workers(): under Dapr the plain Host bypasses the
	// actor invocation daprStore requires and answers ERR_ACTOR_INSTANCE_MISSING,
	// same reasoning as /healthz below.
	if _, err := otel.Meter("farm").Int64ObservableGauge(
		"pipsim.workplace.shift_occupancy",
		otelmetric.WithDescription("Workers currently on shift"),
		otelmetric.WithInt64Callback(func(_ context.Context, o otelmetric.Int64Observer) error {
			o.Observe(int64(svc.Workers()), otelmetric.WithAttributes(
				attribute.String("building_type", "farm"),
			))
			return nil
		}),
	); err != nil {
		slog.Error("could not register shift_occupancy metric", "err", err)
		os.Exit(1)
	}

	// Competing consumers: several replicas share pipsim.work.farm, so an offer
	// goes to exactly one of them. Without a broker URL the farm still serves
	// its RPCs and simply never receives offers.
	//
	// The queue is keyed by kind rather than by building, so the consumer no
	// longer names a workplace id — the host picks which of its farms takes the
	// offer and reports it back through ConsiderOffer. It was still being handed
	// specs[0].ID here, which every outcome then carried regardless of who
	// actually claimed the pip.
	if amqpURL := os.Getenv("AMQP_URL"); amqpURL != "" {
		consumer := queue.NewConsumer(
			amqpURL,
			"farm",
			svc.ConsiderOffer,
		)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go consumer.Run(ctx)
	} else {
		slog.Info("no AMQP_URL set; not consuming work offers")
	}

	mux := http.NewServeMux()
	mux.Handle(workplacev1connect.NewWorkplaceServiceHandler(svc, gotel.WithInterceptor(otelInterceptor)))

	// The endpoints the sidecar calls back into. Disjoint paths from Connect's,
	// so both contracts live on one port. Registered unconditionally: without a
	// sidecar nothing ever calls them, and a mux entry costs nothing.
	mux.Handle("/dapr/", host.Handler())
	mux.Handle("/actors/", host.Handler())

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
