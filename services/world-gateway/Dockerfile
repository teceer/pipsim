# Build context is the REPOSITORY ROOT, not this directory.
#
# The service compiles against the checked-in contracts in gen/go and is tied
# to them by go.work, neither of which lives under services/world-gateway.

FROM golang:1.25-bookworm AS builder

WORKDIR /build

# Manifests first so the module cache survives a source-only change.
COPY go.work ./
COPY gen/go/go.mod gen/go/go.sum ./gen/go/
COPY services/world-gateway/go.mod services/world-gateway/go.sum ./services/world-gateway/
COPY services/workplaces/farm/go.mod ./services/workplaces/farm/
COPY services/workplaces/conformance/go.mod ./services/workplaces/conformance/

RUN go mod download all 2>/dev/null || true

COPY gen/go ./gen/go
COPY services/world-gateway ./services/world-gateway
COPY services/workplaces/farm ./services/workplaces/farm
COPY services/workplaces/conformance ./services/workplaces/conformance

RUN cd services/world-gateway \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gateway ./cmd/gateway

# Static binary, so nothing but certificates is needed at runtime.
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

COPY --from=builder /out/gateway /usr/local/bin/gateway

ENV SIM_CORE_ADDR=sim-core:50051
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/gateway"]
