.PHONY: help build test cover lint tidy docker migrate-up e2e \
        e2e-realstack-up e2e-realstack-down e2e-realstack-test

help:
	@echo "Targets:"
	@echo "  build               — compile the binary"
	@echo "  test                — run the test suite with the race detector"
	@echo "  cover               — run tests with coverage and open the HTML report"
	@echo "  e2e                 — run the end-to-end harness suite (requires Docker)"
	@echo "  e2e-realstack-up    — bring up the H1 real-stack docker-compose stack"
	@echo "  e2e-realstack-down  — tear down the H1 real-stack"
	@echo "  e2e-realstack-test  — run the e2e/realstack/ suite (build tag e2e_realstack)"
	@echo "  lint                — run golangci-lint"
	@echo "  tidy                — run go mod tidy"
	@echo "  docker              — build the container image as fhir-subs:dev"
	@echo "  migrate-up          — apply every embedded migration to \$$DATABASE_URL"

build:
	go build ./cmd/fhir-subs

test:
	go test -race ./...

cover:
	go test -race -coverprofile=cover.out ./...
	go tool cover -html=cover.out

lint:
	golangci-lint run

tidy:
	go mod tidy

docker:
	docker build -t fhir-subs:dev .

migrate-up:
	@# OP #212: shell out to the binary's `migrate up` subcommand so
	@# operators have a single canonical command. DATABASE_URL is
	@# rendered into a minimal probe-only config that satisfies
	@# Validate(); the migrate verb only consumes database.url so the
	@# rest is filler.
	@if [ -z "$$DATABASE_URL" ]; then \
		echo "DATABASE_URL is required" >&2; exit 2; \
	fi
	@tmp_cfg=$$(mktemp /tmp/fhir-subs-migrate.XXXXXX.yaml); \
	trap "rm -f $$tmp_cfg" EXIT; \
	printf '%s\n' \
		"deployment:" \
		"  facility_id: migrate-cli" \
		"  environment: ops" \
		"  log_level: info" \
		"  log_format: json" \
		"  mode: probe-only" \
		"adapter:" \
		"  id: builtin-noop" \
		"server:" \
		"  http:" \
		"    bind: 127.0.0.1:0" \
		"    insecure: true" \
		"lifecycle:" \
		"  shutdown_grace_period: 5s" \
		"database:" \
		"  url: $$DATABASE_URL" \
		"codec:" \
		"  active_key_version: 1" \
		"  keys:" \
		"    - version: 1" \
		"      material: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" \
		"auth:" \
		'  audience: ""' \
		"  allow_dev_bypass: true" \
		"pipeline:" \
		"  hl7_processor:" \
		"    claim_batch_size: 16" \
		"    idle_poll_interval: 100ms" \
		"  matcher:" \
		"    claim_batch_size: 16" \
		"    idle_poll_interval: 100ms" \
		"  submatcher:" \
		"    claim_batch_size: 16" \
		"    idle_poll_interval: 100ms" \
		"  scheduler:" \
		"    claim_batch_size: 16" \
		"    idle_poll_interval: 100ms" \
		"  correlation_hold_window: 1s" \
		> $$tmp_cfg; \
	go run ./cmd/fhir-subs migrate up --config $$tmp_cfg

e2e:
	go test -race -tags e2e ./e2e/... -count=1

# H1 RealStackHarness targets (OP #256). Each target is a thin wrapper
# around docker compose with the realstack project namespace. The Go-
# side harness picks per-test project names, so the realstack-up
# target is for operators bringing up the stack interactively for
# manual exploration.
REALSTACK_PROJECT ?= realstack
REALSTACK_COMPOSE := docker compose -f e2e/realstack/docker-compose.yml -p $(REALSTACK_PROJECT)

e2e-realstack-up:
	$(REALSTACK_COMPOSE) up -d --build --wait

e2e-realstack-down:
	$(REALSTACK_COMPOSE) down -v --remove-orphans

e2e-realstack-test:
	go test -tags e2e_realstack -count=1 ./e2e/realstack/...
