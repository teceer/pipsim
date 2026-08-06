# Spike 0001 — pełna obserwowalność: OTel + Grafana + Prometheus

**Status:** propozycja, do decyzji
**Data:** 2026-08-06
**Kontekst:** `docs/adr/0003-polyglot-with-a-uniform-shell.md` — jednolity kontrakt
operacyjny dla sześciu runtime'ów.

---

## 1. Stan faktyczny

Rozpoznanie repozytorium na dziś:

### Co stoi

| Warstwa | Co | Gdzie |
|---|---|---|
| Collector (dev) | `otel/opentelemetry-collector-contrib:0.111.0` | `compose.dev.yaml:61-65` |
| Collector (k8s) | `otel/opentelemetry-collector-contrib:0.158.0` | `infra/terraform/10-platform/main.tf:380-500` |
| Backend traces | `jaegertracing/all-in-one` (1.62.0 dev / 1.76.0 k8s) | `compose.dev.yaml:67`, `main.tf:307-378` |
| Panel | Jaeger UI, `localhost:16686` | `Makefile:118,126` |

Konfiguracja collectora jest **zduplikowana**: raz jako `infra/otel/collector.yaml`
(montowany w compose), raz jako `yamlencode({...})` w Terraformie. Oba pliki mają
identyczne pipeline'y i identyczny komentarz — czyli rozjadą się przy pierwszej
zmianie, która trafi tylko do jednego.

### Czego nie ma

1. **Instrumentacja to jeden serwis z sześciu.** Wyłącznie `sim-core`
   (`crates/server/src/telemetry.rs`) faktycznie eksportuje spany: OTLP/tonic +
   JSON logi na stdout, plus `current_trace_id()` do stemplowania kopert Kafki.
2. **`world-gateway` i `workplaces/farm` (Go) — zero kodu OTel.** `go.mod` obu
   nie zawiera żadnej zależności `go.opentelemetry.io/*`. Compose wstrzykuje
   `OTEL_EXPORTER_OTLP_ENDPOINT` do gateway (`compose.dev.yaml:93`), ale zmienna
   leci w próżnię. **Skutek: trace ginie na pierwszym hopie z Rusta.**
3. **`broadcast` i `bff` mają tylko deklaracje w manifestach**
   (`mix.exs:29-31` — `opentelemetry ~> 1.5`, `opentelemetry_phoenix ~> 1.2`;
   `package.json:18-19` — `@opentelemetry/sdk-node ^0.54.0`), bez ani jednej
   linii kodu inicjalizującego. Ponadto compose w ogóle nie podaje im
   `OTEL_EXPORTER_OTLP_ENDPOINT`.
4. **Metryki nie istnieją.** Pipeline `metrics` w obu configach eksportuje do
   `debug` — czyli do logów collectora. Brak Prometheusa, brak scrape'owania,
   brak jakiejkolwiek metryki biznesowej (ticki/s, pipy w pracy, głębokość
   kolejki RabbitMQ, lag konsumentów Kafki).
5. **Logi nie są nigdzie agregowane.** JSON na stdout, `kubectl logs` i tyle.
6. **Konwencja `pipsim.<service>.<operation>`** z `CLAUDE.md` nie jest
   egzekwowana poza `sim-core` — nie ma testu ani lintera, który by ją pilnował.

### Praktyczna konsekwencja

`make dev` daje Jaegera, w którym widać wyłącznie spany tickowe `sim-core`.
Żadnego łańcucha Rust → Go → Elixir. Czyli dokładnie ta jedna rzecz, dla której
tracing w polyglotcie ma sens, nie działa.

---

## 2. Rekomendacja: `grafana/otel-lgtm` zamiast składania stosu ręcznie

Zgodnie z zasadą "gotowe rozwiązania nad własną implementacją": nie budujemy
osobno Prometheusa, Grafany, Tempo i provisioningu dashboardów.

**`grafana/otel-lgtm:0.30.0`** to jeden obraz zawierający OTel Collector +
**L**oki + **G**rafana + **T**empo + **M**imir/Prometheus, z gotowym
provisioningiem datasource'ów i korelacją trace↔log↔metric out of the box.
Przyjmuje OTLP na 4317/4318 i sam rozdziela sygnały do właściwych backendów.

**Co to zastępuje:** `otel-collector` + `jaeger` w compose oraz oba w Terraformie.
**Co zyskujemy:** metryki i logi bez dokładania trzech kolejnych komponentów,
plus Grafana jako jeden panel na wszystko.
**Co tracimy:** Jaeger UI (Tempo ma własny explorer w Grafanie — funkcjonalnie
równoważny dla naszych potrzeb) i pełną kontrolę nad configiem collectora
(obraz pozwala nadpisać go przez montowanie własnego).

### ⚠️ Kolizja portów

Grafana słucha na **3000**, a `bff` jest już zmapowany na `3000:3000`
(`compose.dev.yaml`). Grafana musi wyjść na host jako **`3001:3000`**.
To samo w `make infra-forward`.

### Alternatywa dla k8s, jeśli chcemy metryk infrastruktury

`otel-lgtm` nie scrape'uje kubeleta ani node'ów. Jeśli w pewnym momencie chcemy
zobaczyć zużycie CPU podów, `kube-prometheus-stack` (Helm `88.1.5`,
Prometheus Operator `v0.93.0`) daje to plus `ServiceMonitor` per serwis — co
ładnie pasuje do "jednolitego kontraktu operacyjnego": każdy chart deklaruje
własny `ServiceMonitor` i to wszystko.

**Rekomendacja:** faza 1–3 na `otel-lgtm` w obu środowiskach (spójność
dev↔cluster, jeden komponent do ogarnięcia). `kube-prometheus-stack` dopiero
w fazie 5, gdy metryki aplikacyjne będą już płynąć i pojawi się realna potrzeba
patrzenia na infrastrukturę.

---

## 3. Plan wdrożenia

### Faza 1 — platforma (½ dnia)

1. `compose.dev.yaml`: usuń `otel-collector` i `jaeger`, wstaw:
   ```yaml
   observability:
     image: grafana/otel-lgtm:0.30.0
     ports: ["3001:3000", "4317:4317", "4318:4318"]
   ```
2. `infra/terraform/10-platform/main.tf`: zastąp `kubernetes_deployment.jaeger`
   i `kubernetes_deployment.otel_collector` + jego `ConfigMap` jednym
   deploymentem `otel-lgtm`. Zachowaj nazwę Service `otel-collector`, żeby
   `OTEL_EXPORTER_OTLP_ENDPOINT` w chartach nie wymagał zmiany. Output
   `otel_endpoint` zostaje bez zmian.
3. Skasuj `infra/otel/collector.yaml` — duplikacja znika sama.
4. `Makefile`: `jaeger http://localhost:16686` → `grafana http://localhost:3001`,
   port-forward `svc/jaeger 16686` → `svc/otel-collector 3001:3000`.
5. **Kryterium akceptacji:** `make dev` + Grafana na 3001 pokazuje spany
   `sim-core` w Tempo, bez żadnej zmiany w kodzie Rusta.

### Faza 2 — Go: gateway i farm (1 dzień) ← największy zysk

To jest hop, który dziś urywa trace. Biblioteki (wersje sprawdzone dziś):

| Pakiet | Wersja | Po co |
|---|---|---|
| `connectrpc.com/otelconnect` | `v0.9.0` | interceptor Connect-RPC: spany + propagacja W3C, serwer i klient |
| `go.opentelemetry.io/otel` | `v1.45.0` | SDK, `TracerProvider`, `MeterProvider` |
| `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` | `v0.70.0` | HTTP handler/transport |

Punkty wpięcia są czyste, oba serwisy mają identyczną strukturę:

- `services/world-gateway/cmd/gateway/main.go:201-215` — `mux.Handle(worldv1connect.NewWorldServiceHandler(svc))`
- `services/workplaces/farm/cmd/farm/main.go:123-136` — `mux.Handle(workplacev1connect.NewWorkplaceServiceHandler(svc))`

Wystarczy dołożyć `connect.WithInterceptors(otelInterceptor)` do obu wywołań
oraz do klientów wychodzących (gateway → workplaces w `internal/economy/driver.go`,
gateway → sim-core).

**Wspólny pakiet `telemetry`:** dwa serwisy Go potrzebują identycznego
`init()`. Nie kopiuj go — `go.work` już spina moduły, więc naturalne miejsce to
nowy moduł `services/shared/gotel` dopisany do `go.work`. Odpowiednik
`telemetry.rs`: OTLP exporter + `resource.WithAttributes(service.name)` +
`slog` z JSON handlerem + zwrot providera do flushu na shutdown.

**Kryterium akceptacji:** jeden request z przeglądarki do `world-gateway` daje
w Tempo trace z ≥3 spanami z różnych serwisów, spięty jednym trace ID.

### Faza 3 — metryki (1 dzień)

Dopiero teraz Prometheus ma co zbierać.

1. **Go:** `MeterProvider` z OTLP push do collectora (nie pull — mniej
   konfiguracji, nie trzeba `ServiceMonitor`). Runtime metrics przez
   `go.opentelemetry.io/contrib/instrumentation/runtime`.
2. **Rust `sim-core`:** dołóż `opentelemetry-otlp` z feature `metrics`.
   **Uwaga na wersje:** komentarz w `crates/server/Cargo.toml:10-13` ostrzega, że
   `opentelemetry` / `opentelemetry_sdk` / `opentelemetry-otlp` muszą dzielić
   minor (dziś `0.32`), a `tracing-opentelemetry` jest o jeden minor do przodu
   (`0.33`). Metryki dokładamy w ramach tego samego zestawu, bez bumpowania.
3. **Metryki, które faktycznie coś mówią** (nie zaczynaj od golden signals —
   te przyjdą z instrumentacji za darmo):
   - `pipsim.sim.tick_duration` (histogram) — czy 10 Hz się trzyma
   - `pipsim.sim.pips_alive` (gauge)
   - `pipsim.workplace.shift_occupancy` (gauge, po `building_type`)
   - `pipsim.economy.offers_pending` (gauge) — głębokość kolejki RabbitMQ
   - lag konsumentów Kafki
4. **Dashboard** w `infra/grafana/dashboards/pipsim.json`, provisionowany przez
   montowanie do obrazu `otel-lgtm`. Jeden dashboard, commitowany — nie klikany
   w UI i tracony przy restarcie.

### Faza 4 — Elixir, TS, logi (1 dzień)

1. `broadcast`: zależności już są w `mix.exs`, brakuje `OpenTelemetry` setup
   w `application.ex` + `OpentelemetryPhoenix.setup()`. Dopisz
   `OTEL_EXPORTER_OTLP_ENDPOINT` do compose (dziś go nie dostaje).
2. `bff`: `@opentelemetry/sdk-node` w osobnym `instrumentation.ts` ładowanym
   **przed** resztą aplikacji (auto-instrumentacja musi zdążyć podmienić moduły).
   Też brakuje mu zmiennej w compose.
3. `tavern`: brak jakichkolwiek zależności OTel — do dodania od zera, wzorem
   `broadcast`.
4. **Logi → Loki:** `otel-lgtm` ma Loki w środku. Najprościej: wysyłać logi
   przez OTLP zamiast na stdout, wtedy korelacja trace↔log działa automatycznie
   (Grafana linkuje po `trace_id`). To zmienia kontrakt z `CLAUDE.md`
   ("JSON logi na stdout") — **wymaga decyzji: stdout, OTLP, czy oba**.

### Faza 5 — opcjonalnie (½ dnia)

`kube-prometheus-stack` dla metryk kubeleta/node'ów, jeśli okaże się potrzebny.

---

## 4. Otwarte pytania do decyzji

1. **Jaeger wypada?** Tempo w Grafanie jest funkcjonalnie równoważne, ale to
   jednak zmiana narzędzia, do którego przyzwyczaja się palce.
2. **Logi: stdout czy OTLP?** Punkt 4.4 wyżej — kontrakt operacyjny
   w `CLAUDE.md` mówi "JSON logi", nie precyzuje transportu. Wysyłka OTLP daje
   korelację, ale `docker logs` przestaje być użyteczny.
3. **Nowy moduł `services/shared/gotel`** — czy monorepo dopuszcza współdzielony
   moduł Go, czy wolimy duplikację `telemetry.go` w każdym serwisie?
   `CLAUDE.md` mówi "żadna reguła domenowa w dwóch językach", ale telemetria
   nie jest regułą domenową, więc zasada nie rozstrzyga.
4. **Metryki: push (OTLP) czy pull (`/metrics` + scrape)?** Push jest prostszy
   i spójny z tracingiem; pull jest bardziej "prometheusowy" i przetrwa, gdyby
   collector padł. Rekomendacja: push.
5. **ADR?** Jeśli przyjmiemy `otel-lgtm`, warto to zapisać jako ADR-0007 —
   wybór backendu obserwowalności to decyzja architektoniczna, nie detal.

---

## 5. Podsumowanie kosztu

| Faza | Zakres | Estymata |
|---|---|---|
| 1 | Platforma: `otel-lgtm` zamiast collector+jaeger | ½ dnia |
| 2 | Go: gateway + farm, wspólny `gotel` | 1 dzień |
| 3 | Metryki + dashboard | 1 dzień |
| 4 | Elixir, TS, tavern, logi | 1 dzień |
| 5 | `kube-prometheus-stack` (opcjonalnie) | ½ dnia |

**Razem: ~3,5–4 dni.** Faza 2 daje największy zwrot i jest wykonalna
niezależnie od reszty — po niej tracing wreszcie robi to, po co go dodano.
