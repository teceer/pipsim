# Build context is the REPOSITORY ROOT, not this directory.
#
# The service compiles against the checked-in contracts in gen/go and is tied
# to them by go.work, neither of which lives under services/bank.

FROM golang:1.25-bookworm AS builder

WORKDIR /build

COPY go.work ./
COPY gen/go/go.mod gen/go/go.sum ./gen/go/
COPY services/bank/go.mod services/bank/go.sum ./services/bank/

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
