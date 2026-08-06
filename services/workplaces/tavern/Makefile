.DEFAULT_GOAL := build

## run: start the tavern
run:
	mix run --no-halt

## test: unit tests, no cluster required
test:
	mix deps.get --quiet
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
	$(MAKE) -C ../../.. gen-elixir

.PHONY: run test lint build gen
