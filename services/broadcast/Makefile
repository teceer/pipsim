.DEFAULT_GOAL := build

## run: start the Phoenix endpoint
run:
	mix phx.server

## test: unit tests, no cluster required
test:
	mix test

## lint: formatting + credo
lint:
	mix format --check-formatted
	mix credo --strict

## build: production release
build:
	MIX_ENV=prod mix release --overwrite

## gen: contracts are generated centrally into gen/elixir
gen:
	$(MAKE) -C ../.. gen

.PHONY: run test lint build gen
