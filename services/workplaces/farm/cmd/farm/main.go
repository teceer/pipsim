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

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	workplaceID := uint64(envInt("WORKPLACE_ID", 1))
	x := int32(envInt("WORKPLACE_X", 12_000))
	y := int32(envInt("WORKPLACE_Y", 8_000))

	// Position and id are configuration for now. Once BuildWorkplace works,
	// both arrive from the player action that created the building.
	//
	// Shift state goes to Redis when there is a Redis, because Work and EndShift
	// are load-balanced RPCs: a pip hired by one replica has to be known to the
	// next one the gateway happens to reach. Without a URL the farm keeps its
	// shifts in memory and is correct at exactly one replica.
	svc := farm.New(workplaceID, x, y)
	if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
		opts, err := redis.ParseURL(redisURL)
		if err != nil {
			slog.Error("bad REDIS_URL", "err", err)
			os.Exit(1)
		}
		svc = farm.NewWithStore(
			farm.NewRedisStore(redis.NewClient(opts), workplaceID, farm.MaxWorkers,
				farm.ShiftLease, farm.MaxTicksPerWork),
			workplaceID, x, y,
		)
		slog.Info("shift state in redis", "addr", opts.Addr)
	} else {
		slog.Info("no REDIS_URL set; shift state is per-replica")
	}

	// Competing consumers: several replicas share pipsim.work.farm, so an offer
	// goes to exactly one of them. Without a broker URL the farm still serves
	// its RPCs and simply never receives offers.
	if amqpURL := os.Getenv("AMQP_URL"); amqpURL != "" {
		consumer := queue.NewConsumer(
			amqpURL,
			uint64(envInt("WORKPLACE_ID", 1)),
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
