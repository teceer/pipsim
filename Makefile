SHELL := /bin/bash

SERVICES := sim-core world-gateway broadcast bff pathfinder \
            workplaces/farm workplaces/workshop workplaces/tavern

.DEFAULT_GOAL := help

## help: list available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'

# --- service facade ---------------------------------------------------------
# Every service implements the same contract: run/test/lint/build/gen.

## test: run tests for every service
test:
	@for s in $(SERVICES); do \
	  echo "==> $$s"; $(MAKE) -C services/$$s test || exit 1; \
	done

## lint: lint every service
lint:
	@for s in $(SERVICES); do \
	  echo "==> $$s"; $(MAKE) -C services/$$s lint || exit 1; \
	done

## build: build every service
build:
	@for s in $(SERVICES); do \
	  echo "==> $$s"; $(MAKE) -C services/$$s build || exit 1; \
	done

# --- contracts --------------------------------------------------------------

## gen: generate code from proto/ into gen/ (Rust generates itself via build.rs)
# Elixir needs `mix escript.install hex protobuf` on PATH; Go and TS use
# hosted plugins and need nothing installed.
gen:
	buf generate --template proto/buf.gen.yaml -o . proto

## gen-check: verify gen/ is up to date (used in CI)
gen-check: gen
	@git diff --exit-code gen/ || \
	  (echo "gen/ is stale — run 'make gen' and commit the result"; exit 1)

## proto-lint: lint contracts and check backward compatibility
# Run from the repo root, not from proto/: buf resolves the `.git` input
# relative to the working directory, and .git lives here.
proto-lint:
	buf lint proto
	buf breaking proto --against '.git#branch=main,subdir=proto'

# --- development loop -------------------------------------------------------

## dev: bring up platform and services locally, without Kubernetes
dev:
	docker compose -f compose.dev.yaml --profile services up --build

## dev-platform: platform only (redpanda, rabbitmq, redis, postgres)
dev-platform:
	docker compose -f compose.dev.yaml up -d redpanda rabbitmq redis postgres

# --- infrastructure ---------------------------------------------------------
# The ordering is mandatory: the Kafka provider needs a running Kafka to
# initialize, so this cannot be a single apply.

## infra-up: create the cluster and all infrastructure (00 -> 10 -> 20)
infra-up:
	cd infra/terraform/00-cluster  && terraform init && terraform apply -auto-approve
	cd infra/terraform/10-platform && terraform init && terraform apply -auto-approve
	@echo "waiting for the platform to become ready..."
	kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=redpanda \
	  -n pipsim-platform --timeout=300s
	cd infra/terraform/20-data     && terraform init && terraform apply -auto-approve

## infra-down: tear down infrastructure in reverse order
infra-down:
	-cd infra/terraform/20-data     && terraform destroy -auto-approve
	-cd infra/terraform/10-platform && terraform destroy -auto-approve
	-cd infra/terraform/00-cluster  && terraform destroy -auto-approve

## infra-plan: show the plan for every layer
infra-plan:
	@for d in infra/terraform/*/; do \
	  [ -f "$$d/main.tf" ] && (echo "==> $$d"; cd $$d && terraform plan) || true; \
	done

# --- verification -----------------------------------------------------------

## e2e: bring everything up, run 60s of simulation, assert on world state
e2e:
	$(MAKE) dev-platform
	go run ./tools/loadgen -duration 60s -pips 500 -assert

## parity: prove native and WASM simulation cores agree byte for byte
parity:
	./tools/parity/run.sh

## replay: rebuild the world from the Kafka event log
replay:
	go run ./tools/replay -from-beginning

.PHONY: help test lint build gen gen-check proto-lint dev dev-platform \
        infra-up infra-down infra-plan e2e parity replay
