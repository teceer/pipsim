/**
 * OTLP trace export, wired before anything else in this process runs.
 *
 * Must be the first import in index.ts: the Node SDK patches modules at
 * import time, so anything imported before this file — bullmq included — is
 * not instrumented. Service name comes from OTEL_SERVICE_NAME, which the SDK
 * reads itself; there is no bff-specific default because the collector should
 * never see an empty one silently mislabelled as "unknown_service".
 */
import { OTLPTraceExporter } from "@opentelemetry/exporter-trace-otlp-grpc";
import { NodeSDK } from "@opentelemetry/sdk-node";

const sdk = new NodeSDK({
  traceExporter: new OTLPTraceExporter({
    url: process.env.OTEL_EXPORTER_OTLP_ENDPOINT ?? "http://localhost:4317",
  }),
});

sdk.start();

process.on("SIGTERM", () => {
  sdk.shutdown().catch(() => {});
});
