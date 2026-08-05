.DEFAULT_GOAL := build

## run: start the BFF with hot reload
run:
	bun run dev

## test: unit tests, no cluster required
test:
	bun test

## lint: biome + typescript
lint:
	bun run lint
	bunx tsc --noEmit

## build: bundle for production
build:
	bun run build

## gen: contracts are generated centrally into gen/ts
gen:
	$(MAKE) -C ../.. gen

.PHONY: run test lint build gen
