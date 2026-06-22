// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package realstack is the H1 RealStackHarness — the docker-compose
// driven foundation of the fhir-subs e2e suite. Tests use it to boot
// the production cmd/fhir-subs binary as an OS process against a stack
// of real-software dependencies (Postgres 16, Keycloak, HAPI FHIR JPA
// Server, Mailpit) plus three real test subscriber binaries
// (cmd/test-resthook-subscriber, cmd/test-ws-subscriber, and the
// MLLP-profile cmd/test-mllp-control-plane).
//
// This package is the implementation of docs/e2e-coverage-strategy.md
// §3.H1 and the OpenProject story #256. Every other H component (audit
// chain walker, load driver, cert fixture, DNS adversary, observability
// rig, docs lint, prod-stub lint) consumes this harness.
//
// # OP #344 simplification (2026)
//
// The earlier shape of this harness booted 14 services. Half of them
// were testing the env we run in (DNS via CoreDNS, TLS termination via
// nginx, MITM transport via mitmproxy) instead of testing the
// production binary, and the metrics/trace assertions were running
// full Prometheus + OTel Collector containers to assert behavior the
// binary already exposes through narrow Go-level seams. OP #344
// deleted seven services (prometheus, otel-collector, otel-spans
// sidecar, coredns, mitmproxy, nginx, test-token-mint) and moved
// every assertion to a proper unit/integration test:
//
//   - "binary emits metric X" -> cmd/fhir-subs/wiring_observability_test.go::
//     TestProductionRuntime_MountsMetricsEndpoint boots the production
//     runtime, GETs /metrics, and asserts every fhir_subs_api_*
//     counter the metrics package declares is present in the
//     Prometheus exposition. No prometheus container.
//
//   - "binary emits span X" -> internal/infra/observability/tracing/
//     tracing_test.go::TestNew_OTLPHTTPExporter_DeliversSpansToCollector
//     stands up a real net/http OTLP receiver in-process, points the
//     binary's tracing module at it, and asserts span batches actually
//     reach /v1/traces. Plus the existing TestNew_WithInMemoryExporter
//     pins the SpanExporter seam end-to-end. No otel-collector
//     container.
//
//   - SSRF / DNS-rebinding / IDN-homoglyph / metadata-IP / CG-NAT ->
//     internal/api/handlers/url_validator_test.go drives the
//     production validator through the existing handlers.Resolver
//     seam (staticResolver, blockingResolver) so DNS-rebinding,
//     loopback-canonicalization, and metadata-IP rejection are pinned
//     against the real validator implementation. No CoreDNS container.
//
//   - STARTTLS-strip -> internal/channel/email/email_test.go's
//     TestSTARTTLSRequiredButUnsupported drives the production email
//     channel through an in-process testrelay that does not advertise
//     STARTTLS. STARTTLS=required must refuse to send when the relay
//     fails to upgrade — that is exactly the strip-STARTTLS surface
//     mitmproxy used to simulate. No mitmproxy container.
//
//   - TLS termination via nginx -> deleted entirely. Testing nginx is
//     not testing the binary; if a real assertion about the binary's
//     behavior behind a TLS terminator is needed, it lives as a unit
//     test on the relevant handler.
//
// The simplified harness keeps seven services: postgres, keycloak,
// hapi-fhir, mailpit, test-resthook-subscriber, test-ws-subscriber,
// and the MLLP-profile test-mllp-control-plane. Boot is faster, the
// flake surface is smaller, and the assertions live closer to the
// code they pin. Story-B and story-C continue the simplification by
// combining the four receivers and env-gating external systems; this
// story is the structural deletion pass.
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
