//! Tracing and OpenTelemetry setup.
//!
//! Two sinks, deliberately: JSON logs on stdout matching the schema every
//! service in this repo uses, and OTLP spans to the collector. With six
//! languages in the project, the collector is the only place a request crossing
//! Rust, Go, Elixir and TypeScript can be read as one chain — which is why this
//! is wired before anything else runs, not bolted on later.

use anyhow::Result;
use opentelemetry::trace::TracerProvider as _;
use opentelemetry::KeyValue;
use opentelemetry_otlp::WithExportConfig;
use opentelemetry_sdk::trace::SdkTracerProvider;
use opentelemetry_sdk::Resource;
use tracing_subscriber::layer::SubscriberExt;
use tracing_subscriber::util::SubscriberInitExt;
use tracing_subscriber::EnvFilter;

pub const SERVICE_NAME: &str = "sim-core";

/// Returns the provider so `main` can flush it on shutdown. Dropping spans on
/// exit is the classic way to spend an hour wondering why the last trace never
/// showed up in Jaeger.
pub fn init() -> Result<SdkTracerProvider> {
    let endpoint = std::env::var("OTEL_EXPORTER_OTLP_ENDPOINT")
        .unwrap_or_else(|_| "http://localhost:4317".to_string());

    let exporter = opentelemetry_otlp::SpanExporter::builder()
        .with_tonic()
        .with_endpoint(&endpoint)
        .build()?;

    let provider = SdkTracerProvider::builder()
        .with_batch_exporter(exporter)
        .with_resource(
            Resource::builder()
                .with_service_name(SERVICE_NAME)
                .with_attributes([KeyValue::new("deployment.environment", "local")])
                .build(),
        )
        .build();

    let tracer = provider.tracer(SERVICE_NAME);

    tracing_subscriber::registry()
        // Ticks run at 10 Hz; at DEBUG this would emit a span every 100 ms
        // forever, which buries anything interesting.
        .with(EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info")))
        .with(
            tracing_subscriber::fmt::layer()
                .json()
                .with_current_span(true),
        )
        .with(tracing_opentelemetry::layer().with_tracer(tracer))
        .init();

    tracing::info!(otlp_endpoint = %endpoint, "telemetry initialised");
    Ok(provider)
}

/// W3C trace id of the current span, for stamping onto Kafka envelopes.
///
/// This is what ties an event sitting in a topic back to the tick that produced
/// it: a consumer three services away can take this id straight to Jaeger.
pub fn current_trace_id() -> String {
    use opentelemetry::trace::TraceContextExt;
    use tracing_opentelemetry::OpenTelemetrySpanExt;

    let ctx = tracing::Span::current().context();
    let span = ctx.span();
    let sc = span.span_context();
    if sc.is_valid() {
        sc.trace_id().to_string()
    } else {
        String::new()
    }
}
