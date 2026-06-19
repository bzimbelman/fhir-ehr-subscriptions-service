// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package realstack is the H1 RealStackHarness — the docker-compose
// driven foundation of the fhir-subs e2e suite. Tests use it to boot
// the production cmd/fhir-subs binary as an OS process against a stack
// of real-software dependencies (Postgres 16, Keycloak, HAPI FHIR JPA
// Server, Mailpit, Prometheus, OTel Collector, CoreDNS, nginx,
// mitmproxy) plus two real test subscriber binaries
// (cmd/test-resthook-subscriber, cmd/test-ws-subscriber).
//
// This package is the implementation of docs/e2e-coverage-strategy.md
// §3.H1 and the OpenProject story #256. Every other H component (audit
// chain walker, load driver, cert fixture, DNS adversary, observability
// rig, docs lint, prod-stub lint) consumes this harness.
//
// # No fakes
//
// The package contains no Go-language fakes, stubs, or mocks. Every
// dependency is brought up as real software via docker-compose; the
// harness is a thin Go-side client that:
//
//  1. Picks a unique compose project name (per test).
//  2. Runs `docker compose -p <project> -f e2e/realstack/docker-compose.yml up -d`.
//  3. Polls each real service's real /health probe until it reports ready.
//  4. Renders a config file pointing the prod binary at every real
//     service's bound host:port.
//  5. Launches `cmd/fhir-subs` via exec.Cmd as a real child process.
//  6. Waits for the binary's real /readyz to return 200.
//  7. Returns a Stack handle that lets tests issue real HTTP / MLLP /
//     WS / SMTP requests and query each real dependency's real API for
//     captured state.
//
// On Close, the harness runs `docker compose down -v` against the
// per-test project namespace, leaving no containers, volumes, or
// networks behind.
//
// # Build tag
//
// Files in this package are gated behind the `e2e_realstack` build tag
// so `go build ./...` and `go test ./...` skip the harness by default.
// CI runs the harness suite with:
//
//	go test -tags e2e_realstack -count=1 ./e2e/realstack/...
//
// # Concurrency
//
// Each Boot picks a unique compose project name (UUID-derived). Two
// concurrent Boot calls get two completely-isolated stacks: distinct
// containers, distinct networks, distinct port bindings. The stack is
// designed to support >50 concurrent test scenarios on a single CI
// runner.
package realstack
