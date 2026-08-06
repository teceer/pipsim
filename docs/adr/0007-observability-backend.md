# 7. Observability backend: `grafana/otel-lgtm`, no Jaeger, metrics and logs over OTLP

Status: accepted

## Context

`docs/spikes/0001-otel-grafana-prometheus.md` audited the state of tracing across the six
runtimes. The findings, in short:

- The OTel Collector config was **duplicated** — once as `infra/otel/collector.yaml`, once
  as `yamlencode({...})` in `infra/terraform/10-platform/main.tf` — with identical pipelines
  that would drift the moment one was edited and the other was not.
- Only `sim-core` actually exported spans. `world-gateway` and `workplaces/farm` had zero
  OTel code despite `go.mod` declaring nothing and compose injecting
  `OTEL_EXPORTER_OTLP_ENDPOINT` into a service that never read it — the trace died on the
  first hop out of Rust. `broadcast` and `workplaces/tavern` had library declarations and no
  init code at all.
- Metrics did not exist: both collector pipelines routed `metrics` to a `debug` exporter,
  i.e. into the collector's own logs. No Prometheus, no business metric.
- Logs were JSON on stdout only, `kubectl logs` and nothing else.

## Decision

**Replace the collector + Jaeger with `grafana/otel-lgtm:0.30.0`**, one image bundling the
OTel Collector, Loki, Grafana, Tempo and Mimir/Prometheus, with datasource provisioning and
trace↔log↔metric correlation built in. This is `[[prefer-off-the-shelf-over-custom]]`
applied to the observability stack: integrate a production tool rather than keep two
hand-maintained collector configs in sync by hand.

- **compose.dev.yaml** and **`infra/terraform/10-platform/main.tf`**: `otel-collector` +
  `jaeger` become one `observability` / `otel-lgtm` deployment. The Kubernetes Service keeps
  the name `otel-collector`, so `OTEL_EXPORTER_OTLP_ENDPOINT` in every chart needs no change.
  `infra/otel/collector.yaml` is deleted — the duplication is gone because there is nothing
  left to duplicate.
- **Jaeger is retired.** Tempo's explorer in Grafana is the functional replacement. The one
  thing lost is UI muscle memory, not capability.
- **Every Go service shares one `gotel.Init`** (`services/shared/gotel`), added to `go.work`
  rather than copied into `world-gateway` and `farm` separately — telemetry wiring is a
  shared *mechanism*, not a domain rule, so rule 4 in the root `CLAUDE.md` does not apply to
  it. It sets up JSON logs, an OTLP trace exporter, an OTLP metric exporter and the W3C
  propagator in one call, and hands back a Connect interceptor
  (`connectrpc.com/otelconnect`) both handlers and clients install.
- **Metrics push over OTLP, not pull.** No `/metrics` endpoint, no `ServiceMonitor` to keep
  in sync between compose and the cluster — the same transport traces already use.
- **Elixir gets a hand-written span, not a framework hook.** `broadcast` has no Phoenix
  telemetry events to attach `opentelemetry_phoenix` to — it is still unimplemented, only
  declared in `mix.exs` (see Consequences). `workplaces/tavern` hand-rolls Connect over Plug
  and Bandit, so `Tavern.Connect.call/2` wraps the RPC dispatch in
  `Tracer.with_span "pipsim.tavern.<method>"` itself, the same way `otelconnect` does it for
  Go and `tracing-opentelemetry` does it for Rust.
- **Logs stay on stdout.** OTLP log export was the one genuinely open question in the spike
  (`docs/spikes/0001-otel-grafana-prometheus.md`, open question 2) — it buys trace↔log
  correlation in Grafana but costs `docker logs`/`kubectl logs` as a working debugger. Given
  the JSON-on-stdout contract in the root `CLAUDE.md` is silent on transport, and every
  service in this repo has been debugged via structured stdout since before this ADR, the
  decision is to leave logs on stdout unless correlation becomes a recurring need — Loki
  exists in the stack already if that changes, and it costs one exporter per service, not a
  new component.

## Why not build the stack piece by piece

Prometheus + Tempo + Loki + Grafana + a hand-provisioned dashboard is roughly what
`otel-lgtm` already is, minus the coordination of getting four datasources to agree on
correlation IDs. Nothing about running Prometheus by hand teaches something about this
project's actual problem — polyglot tracing — that assembling the same stack from an image
built for exactly this purpose does not. This mirrors the reasoning in ADR 0005 for choosing
Dapr over hand-rolling an actor runtime: integrate the production tool, reserve hand-rolling
for the part of the stack the project exists to study.

## Why not keep Jaeger

Tempo, inside the same image, is the direct functional replacement and removes a second
trace backend from the compose file and the cluster for free. The cost is real but small: a
UI that people's fingers already know.

## Consequences

- **Grafana takes host port 3001, not 3000** — `bff` already owns 3000 in
  `compose.dev.yaml`. `make infra-forward` and the Makefile's status line were updated to
  match.
- **`kube-prometheus-stack` is deferred, not rejected.** `otel-lgtm` does not scrape kubelet
  or node metrics. If infrastructure-level metrics (pod CPU, memory) become a real need, that
  is a separate Helm release layered on top — Prometheus Operator's `ServiceMonitor` per
  chart fits the "identical Make targets, identical operational contract" shape this repo
  already uses everywhere else.
- **`broadcast` still has no OpenTelemetry code**, because it still has no code —
  `mix.exs` declares `mod: {Broadcast.Application, []}` and that module does not exist yet.
  Wiring telemetry into an application that has not been built is not a spike outcome, it is
  a second project; `OTEL_EXPORTER_OTLP_ENDPOINT` was added to its compose entry so the day
  `Broadcast.Application` exists, only `application.ex` needs to change.
- **No Kafka consumer-group lag metric.** The spike's business-metric list included one, but
  nothing in this repo consumes Kafka yet — `world-gateway` only produces. A lag gauge with
  nothing to measure is a metric that is always zero, which is worse than an honest gap; this
  is deferred to whichever service becomes the first real consumer.
- **A new shared Go module needs `go.work`, not `go.mod` requires, to build offline.**
  `services/shared/gotel` has no tagged commit on the remote (`github.com/teceer/pipsim`)
  yet, so `go.mod` cannot carry a real pseudo-version for it the way `gen/go` does. Workspace
  mode resolves it from the `use` list directly — no `require` line and no `replace` needed,
  which matches the existing comment in `go.work` about not needing replace directives.
- **`pipsim.<service>.<operation>` span naming is now enforced by code, not just by
  documentation**, for every service that emits a span at all — `otelconnect` and
  `tracing-opentelemetry` name Connect/gRPC spans by method already, and
  `Tavern.Connect` now names its span explicitly to match. Whether the convention holds is
  still not checked by a test or a linter; that gap from the spike (`docs/spikes/0001-…`,
  finding 6) stands.

## Dashboard

`infra/grafana/dashboards/pipsim.json`, provisioned into the image by mounting it (plus
`infra/grafana/dashboards-provisioning.yaml`) at
`/otel-lgtm/grafana/conf/provisioning/dashboards/`, in both compose and the Terraform
`ConfigMap` — committed rather than clicked together in the UI and lost on restart. Panels:
`pipsim.sim.tick_duration` (p50/p95, whether the 10 Hz loop holds), `pipsim.sim.pips_alive`,
`pipsim.workplace.shift_occupancy` by `building_type`, `pipsim.economy.offers_pending` by
`building_type` (RabbitMQ work-queue depth via passive `QueueInspect`, not a broker-level
exporter). The exact Prometheus metric names depend on the OTel-to-Prometheus translation
`otel-lgtm`'s bundled collector applies (dots to underscores, unit suffixes on histograms) —
verify against a running stack before trusting the panel queries literally.
