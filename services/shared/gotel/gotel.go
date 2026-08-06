// Package gotel is the one init every Go service in this repo shares:
// JSON logs on stdout plus OTLP spans to the collector. With six languages in
// the project, the collector is the only place a request crossing Rust, Go,
// Elixir and TypeScript can be read as one chain — the Rust equivalent is
// crates/server/src/telemetry.rs.
//
// This is a shared *mechanism*, not a domain rule, so rule 4 in the root
// CLAUDE.md ("no domain rule in two languages") does not apply to it — the two
// Go services would otherwise carry byte-identical copies of this file.
package gotel

import (
	"context"
	"log/slog"
	"os"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Init sets the default slog handler to JSON on stdout, wires OTLP trace and
// metric exporters and installs them as the global providers, plus the W3C
// propagator. Metrics push on the same schedule traces do — no `/metrics`
// endpoint, no `ServiceMonitor` to keep in sync between compose and the
// cluster.
//
// Callers must defer the returned shutdown func — a provider that never
// flushes drops whatever batch was in flight when the process exited, which
// looks like a trace or metric that simply never showed up in Grafana.
func Init(ctx context.Context, serviceName string) (shutdown func(context.Context) error, err error) {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	endpoint := env("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")

	traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpointURL(endpoint))
	if err != nil {
		return nil, err
	}

	metricExporter, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithEndpointURL(endpoint))
	if err != nil {
		return nil, err
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.DeploymentEnvironment("local"),
		),
	)
	if err != nil {
		return nil, err
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tracerProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(meterProvider)

	slog.Info("telemetry initialised", "service", serviceName, "otlp_endpoint", endpoint)
	return func(ctx context.Context) error {
		err := tracerProvider.Shutdown(ctx)
		if merr := meterProvider.Shutdown(ctx); merr != nil {
			err = merr
		}
		return err
	}, nil
}

// Interceptor builds the Connect interceptor every handler and client in this
// repo installs: spans plus W3C trace propagation, identical whether the
// process is serving RPCs or driving them.
func Interceptor() (connect.Interceptor, error) {
	return otelconnect.NewInterceptor()
}

// WithInterceptor is shorthand for the connect.ClientOption/HandlerOption both
// NewXHandler and NewXClient accept, so call sites do not have to import
// otelconnect just to spell WithInterceptors(interceptor).
func WithInterceptor(interceptor connect.Interceptor) connect.Option {
	return connect.WithInterceptors(interceptor)
}
