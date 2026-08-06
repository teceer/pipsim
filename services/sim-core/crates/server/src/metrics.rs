//! Metrics: the business signals that say whether the simulation is healthy,
//! pushed over OTLP to the same collector traces go to. Push rather than pull
//! — no `ServiceMonitor`, no `/metrics` scrape config, one less thing to keep
//! in sync between compose and the cluster.

use anyhow::Result;
use opentelemetry::metrics::{Gauge, Histogram, MeterProvider as _};
use opentelemetry::KeyValue;
use opentelemetry_otlp::WithExportConfig;
use opentelemetry_sdk::metrics::SdkMeterProvider;
use opentelemetry_sdk::Resource;

use crate::telemetry::SERVICE_NAME;

pub struct Metrics {
    /// Seconds to compute and publish one tick. What tells us 10 Hz is
    /// actually holding under load.
    pub tick_duration: Histogram<f64>,
    /// Pips currently alive in the world.
    pub pips_alive: Gauge<u64>,
}

/// Returns the provider so `main` can flush it on shutdown, same reasoning as
/// `telemetry::init`.
pub fn init() -> Result<(SdkMeterProvider, Metrics)> {
    let endpoint = std::env::var("OTEL_EXPORTER_OTLP_ENDPOINT")
        .unwrap_or_else(|_| "http://localhost:4317".to_string());

    let exporter = opentelemetry_otlp::MetricExporter::builder()
        .with_tonic()
        .with_endpoint(&endpoint)
        .build()?;

    let provider = SdkMeterProvider::builder()
        .with_periodic_exporter(exporter)
        .with_resource(
            Resource::builder()
                .with_service_name(SERVICE_NAME)
                .with_attributes([KeyValue::new("deployment.environment", "local")])
                .build(),
        )
        .build();

    opentelemetry::global::set_meter_provider(provider.clone());

    let meter = provider.meter(SERVICE_NAME);
    let tick_duration = meter
        .f64_histogram("pipsim.sim.tick_duration")
        .with_unit("s")
        .with_description("Wall time to compute and publish one tick")
        .build();
    let pips_alive = meter
        .u64_gauge("pipsim.sim.pips_alive")
        .with_description("Pips currently alive in the world")
        .build();

    Ok((provider, Metrics {
        tick_duration,
        pips_alive,
    }))
}
