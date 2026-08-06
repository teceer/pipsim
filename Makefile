SHELL := /bin/bash

SERVICES := sim-core world-gateway bank broadcast bff pathfinder \
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
gen: gen-elixir
	buf generate --template proto/buf.gen.yaml -o . proto

## gen-elixir: Elixir contracts, generated with protoc rather than buf
#
# protoc-gen-elixir writes its output under the proto *package* path, and protoc
# has already placed it under the source path — which are the same thing here,
# so every file lands at pips/x/v1/pips/x/v1/name.pb.ex. That is the duplication
# gen/README.md blamed on buf; it is the plugin, and buf is not involved below.
#
# Generating into a scratch directory and lifting the inner tree out is less
# clever than fighting the plugin's path logic, and it does not care if a future
# version stops doubling.
gen-elixir:
	@command -v protoc-gen-elixir >/dev/null || { \
	  echo "protoc-gen-elixir missing: mix escript.install hex protobuf"; exit 1; }
	@tmp=$$(mktemp -d) && \
	  protoc -I proto --elixir_out=$$tmp $(shell find proto -name '*.proto') && \
	  rm -rf gen/elixir/pips && \
	  find $$tmp -name '*.pb.ex' | while read -r f; do \
	    rel=$${f#$$tmp/}; \
	    dest="gen/elixir/$${rel#*/pips/}"; \
	    dest="gen/elixir/pips/$${dest#gen/elixir/}"; \
	    mkdir -p "$$(dirname "$$dest")" && cp "$$f" "$$dest"; \
	  done && \
	  rm -rf $$tmp

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

## infra-forward: port-forward the platform UIs and admin APIs to localhost
# Kafka needs no forward — layer 00 maps its NodePort straight to the host.
# The rest are ClusterIP, and layer 20's rabbitmq/postgres providers talk to
# them over these forwards, so this must be running before `make infra-up`
# reaches layer 20.
#
# world-gateway belongs on this list even though it is not platform: it is the
# only address the browser client knows (VITE_GATEWAY_URL, default :8081), and
# without it the client silently falls back to its local WASM world and reports
# "local (no gateway)". Note that a forward dies with the pod behind it, so a
# rollout of any of these means restarting this target.
infra-forward:
	@echo "grafana         http://localhost:3001"
	@echo "redpanda console http://localhost:8085"
	@echo "rabbitmq        http://localhost:15672  (pipsim/pipsim)"
	@echo "kafka           localhost:31092"
	@echo "postgres        localhost:5432          (pipsim/pipsim)"
	@echo "world-gateway   http://localhost:8081   (what the web client dials)"
	@echo
	@kubectl -n pipsim port-forward svc/world-gateway 8081:8081 & \
	 kubectl -n pipsim-platform port-forward svc/otel-collector 3001:3000 & \
	 kubectl -n pipsim-platform port-forward svc/redpanda-console 8085:8080 & \
	 kubectl -n pipsim-platform port-forward svc/rabbitmq 15672:15672 & \
	 kubectl -n pipsim-platform port-forward svc/postgres 5432:5432 & \
	 wait

## infra-recreate: destroy and recreate the cluster
# The k3d provider cannot update a cluster in place and does not mark its
# config as force-new, so changing anything in layer 00 needs an explicit
# destroy. Layer 10 goes first because its providers need a live cluster.
infra-recreate:
	-cd infra/terraform/10-platform && terraform destroy -auto-approve
	cd infra/terraform/00-cluster && terraform destroy -auto-approve
	$(MAKE) infra-up

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
        infra-up infra-down infra-plan infra-forward infra-recreate e2e parity replay
