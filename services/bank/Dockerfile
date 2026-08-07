# Build context is the REPOSITORY ROOT, not this directory.
#
# The service compiles against the checked-in contracts in gen/go and is tied
# to them by go.work, neither of which lives under services/bank.

FROM golang:1.25-bookworm AS builder

WORKDIR /build

COPY go.work ./
# Every module in go.work must be present, or `go build` refuses to start:
# the workspace is loaded before anything is compiled, and a member it cannot
# open is a hard error even when nothing imports it. Manifests only for the
# ones this image does not build — they cost a few kilobytes and save a
# confusing failure whenever a new service joins the workspace.
COPY gen/go/go.mod gen/go/go.sum ./gen/go/
COPY services/bank/go.mod services/bank/go.sum ./services/bank/
COPY services/shared/gotel/go.mod services/shared/gotel/go.sum ./services/shared/gotel/
COPY services/workplaces/conformance/go.mod services/workplaces/conformance/go.sum ./services/workplaces/conformance/
COPY services/workplaces/farm/go.mod services/workplaces/farm/go.sum ./services/workplaces/farm/
COPY services/world-gateway/go.mod services/world-gateway/go.sum ./services/world-gateway/

RUN go mod download all 2>/dev/null || true

COPY gen/go ./gen/go
COPY services/bank ./services/bank

RUN cd services/bank \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bank ./cmd/bank

FROM gcr.io/distroless/static-debian12:nonroot AS runtime

COPY --from=builder /out/bank /usr/local/bin/bank

ENV DATABASE_URL=postgres://pipsim:pipsim@postgres:5432/pipsim
EXPOSE 8082
ENTRYPOINT ["/usr/local/bin/bank"]
