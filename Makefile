.PHONY: help build test cover lint tidy docker migrate-up e2e

help:
	@echo "Targets:"
	@echo "  build       — compile the binary"
	@echo "  test        — run the test suite with the race detector"
	@echo "  cover       — run tests with coverage and open the HTML report"
	@echo "  e2e         — run the end-to-end harness suite (requires Docker)"
	@echo "  lint        — run golangci-lint"
	@echo "  tidy        — run go mod tidy"
	@echo "  docker      — build the container image as fhir-subs:dev"
	@echo "  migrate-up  — apply the v0 schema (placeholder until the runner is wired)"

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
	@echo "TODO: wire the migration runner. For now: psql $$DATABASE_URL -f migrations/0001_init.sql"

e2e:
	go test -race -tags e2e ./e2e/... -count=1
