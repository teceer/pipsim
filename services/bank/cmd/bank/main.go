// Command bank serves pips.bank.v1.BankService.
//
// The double-entry ledger for the whole simulation: every actor has exactly
// one account, every movement of money is a transfer between two of them.
// See docs/adr/0006 for why this is its own service rather than a table
// inside sim-core.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/teceer/pipsim/gen/go/pips/bank/v1/bankv1connect"
	"github.com/teceer/pipsim/services/bank/internal/bankapi"
	"github.com/teceer/pipsim/services/bank/internal/ledger"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func kafkaBrokers() []string {
	var out []string
	for _, b := range strings.Split(os.Getenv("KAFKA_BROKERS"), ",") {
		if b = strings.TrimSpace(b); b != "" {
			out = append(out, b)
		}
	}
	return out
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	databaseURL := env("DATABASE_URL", "postgres://pipsim:pipsim@localhost:5432/pipsim")
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		slog.Error("could not create the postgres pool", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	migrateCtx, cancelMigrate := context.WithTimeout(ctx, 30*time.Second)
	if err := ledger.Migrate(migrateCtx, pool); err != nil {
		cancelMigrate()
		slog.Error("could not migrate the bank schema", "err", err)
		os.Exit(1)
	}
	cancelMigrate()

	var events bankapi.EventPublisher
	if brokers := kafkaBrokers(); len(brokers) > 0 {
		kafkaEvents := bankapi.NewKafkaEvents(brokers)
		defer func() { _ = kafkaEvents.Close() }()
		events = kafkaEvents
		slog.Info("publishing money facts", "brokers", brokers)
	} else {
		slog.Info("KAFKA_BROKERS not set; money facts will not be published")
	}

	handler := bankapi.New(ledger.NewPostgres(pool), events)

	mux := http.NewServeMux()
	mux.Handle(bankv1connect.NewBankServiceHandler(handler))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	addr := env("BANK_ADDR", ":8082")
	srv := &http.Server{
		Addr:              addr,
		Handler:           h2c.NewHandler(mux, &http2.Server{}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("bank listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown failed", "err", err)
	}
	slog.Info("bank stopped")
}
