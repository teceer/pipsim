.DEFAULT_GOAL := build

## run: start the farm
run:
	go run ./cmd/farm

## test: unit tests, no cluster required
test:
	go test ./... -race -count=1

## lint: vet + golangci-lint
lint:
	go vet ./...
	golangci-lint run

## build: static binary
build:
	CGO_ENABLED=0 go build -o bin/farm ./cmd/farm

## gen: contracts are generated centrally into gen/go
gen:
	$(MAKE) -C ../../.. gen

.PHONY: run test lint build gen
