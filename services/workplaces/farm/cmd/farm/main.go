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
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/teceer/pipsim/gen/go/pips/workplace/v1/workplacev1connect"
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

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

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

	// Shift state goes to Redis when there is a Redis, because Work and EndShift
	// are load-balanced RPCs: a pip hired by one replica has to be known to the
	// next one the gateway happens to reach. Without a URL the farm keeps its
	// shifts in memory and is correct at exactly one replica.
	//
	// One store per building, never one shared: capacity is a property of a
	// building, and a store spanning two of them would enforce 24 workers
	// across the pair.
	var rdb *redis.Client
	if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
		opts, err := redis.ParseURL(redisURL)
		if err != nil {
			slog.Error("bad REDIS_URL", "err", err)
			os.Exit(1)
		}
		rdb = redis.NewClient(opts)
		slog.Info("shift state in redis", "addr", opts.Addr)
	} else {
		slog.Info("no REDIS_URL set; shift state is per-replica")
	}

	buildings := make([]*farm.Service, 0, len(specs))
	for _, sp := range specs {
		if rdb == nil {
			buildings = append(buildings, farm.New(sp.ID, sp.X, sp.Y))
			continue
		}
		buildings = append(buildings, farm.NewWithStore(
			farm.NewRedisStore(rdb, sp.ID, farm.MaxWorkers,
				farm.ShiftLease, farm.MaxTicksPerWork),
			sp.ID, sp.X, sp.Y,
		))
	}
	svc := farm.NewHost(buildings...)

	// Competing consumers: several replicas share pipsim.work.farm, so an offer
	// goes to exactly one of them. Without a broker URL the farm still serves
	// its RPCs and simply never receives offers.
	//
	// The queue is keyed by kind rather than by building, so the consumer no
	// longer names a workplace id — the host picks which of its farms takes the
	// offer.
	if amqpURL := os.Getenv("AMQP_URL"); amqpURL != "" {
		consumer := queue.NewConsumer(
			amqpURL,
			specs[0].ID,
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
