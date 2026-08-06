.DEFAULT_GOAL := build

## run: start the bank (needs DATABASE_URL reachable)
run:
	go run ./cmd/bank

## test: unit tests against the in-memory ledger, no Postgres required
test:
	go test ./... -race -count=1

## lint: vet + golangci-lint
lint:
	go vet ./...
	golangci-lint run

## build: static binary
build:
	CGO_ENABLED=0 go build -o bin/bank ./cmd/bank

## gen: contracts are generated centrally into gen/go
gen:
	$(MAKE) -C ../.. gen

.PHONY: run test lint build gen
