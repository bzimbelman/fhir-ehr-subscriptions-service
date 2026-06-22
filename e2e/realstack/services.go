// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

package realstack

// Service handles. Each value is populated by Boot from the docker
// compose port mappings and the rendered config the harness wrote for
// the prod binary. The Addr fields are always host:port pairs reachable
// from the test process; URL/IssuerURL fields are full base URLs
// pointing at the real-software service running in docker-compose.
//
// Every field corresponds to a real container in
// e2e/realstack/docker-compose.yml. The harness fails Boot if any of
// the services don't report healthy within the boot timeout — there is
// no "service unavailable" path that yields an empty handle.

// PostgresHandle exposes the real Postgres 16 container.
type PostgresHandle struct {
	// Addr is host:port where the test process can connect via pgx.
	Addr string
	// URL is the full postgres:// connection string the prod binary
	// receives via its rendered config (database.url). Includes
	// credentials, dbname, sslmode=disable.
	URL string
}

// KeycloakHandle exposes the real Keycloak 24 container.
type KeycloakHandle struct {
	// Addr is host:port for the Keycloak admin/REST endpoints.
	Addr string
	// IssuerURL is the OIDC issuer the prod binary's verifier fetches
	// JWKS from. Format: http://<addr>/realms/fhir-subs.
	IssuerURL string
	// JWKSURL is the full JWKS endpoint URL.
	JWKSURL string
	// TokenURL is Keycloak's real token endpoint.
	TokenURL string
	// Realm is the realm name the harness provisioned (default: fhir-subs).
	Realm string
}

// HAPIFHIRHandle exposes the real HAPI FHIR JPA Server container.
type HAPIFHIRHandle struct {
	// Addr is host:port for the FHIR server.
	Addr string
	// BaseURL is the FHIR base URL (http://<addr>/fhir).
	BaseURL string
}

// MailpitHandle exposes the real Mailpit SMTP relay + REST API.
type MailpitHandle struct {
	// SMTPAddr is host:port the email channel sends to.
	SMTPAddr string
	// APIAddr is the host:port serving Mailpit's REST API.
	APIAddr string
	// APIBaseURL is the Mailpit REST API base URL for captured-message assertions.
	APIBaseURL string
}

// OP #344: PrometheusHandle, OTelHandle, CoreDNSHandle, NginxHandle, and
// MitmproxyHandle were deleted alongside the corresponding compose
// services. Their assertions live in proper unit/integration tests:
//
//   - /metrics surfacing named counters: cmd/fhir-subs/wiring_observability_test.go
//     (boots the real productionRuntime, scrapes /metrics, asserts the
//     fhir_subs_api_* counter set is registered)
//   - tracing pipeline span emission: internal/api/handlers/tracing_test.go
//     (TracingMiddleware against tracetest.SpanRecorder) +
//     internal/infra/observability/tracing/tracing_test.go (Module wired
//     through tracetest.NewInMemoryExporter)
//   - SSRF / DNS-rebinding / IDN / metadata-IP / CG-NAT:
//     internal/api/handlers/url_validator_test.go (against the existing
//     handlers.Resolver seam — staticResolver, blockingResolver)
//   - STARTTLS-strip: internal/channel/email/email_test.go
//     (TestSTARTTLSRequiredButUnsupported drives an in-process testrelay
//     that does not advertise STARTTLS — the production policy refuses
//     to send when STARTTLS=required is asked for and the relay strips
//     the upgrade, which is exactly the strip-STARTTLS surface mitmproxy
//     used to simulate)
//   - TLS-terminator topology: deleted entirely; testing nginx is not
//     testing the binary.

// SubscriberHandle is shared by the rest-hook and websocket test
// subscriber binaries. Each runs as a real container; the harness
// queries QueryAPIURL to assert on captured deliveries.
type SubscriberHandle struct {
	// Addr is host:port the binary's channel dials when delivering events.
	Addr string
	// EndpointURL is the full URL the operator should configure in the
	// Subscription resource (e.g. http://<addr>/notify for the rest-hook
	// subscriber, ws://<addr>/ws for the websocket subscriber).
	EndpointURL string
	// QueryAPIURL is the captured-state REST API exposed by the
	// subscriber binary. Tests GET <QueryAPIURL>/notifications to read
	// every delivery the subscriber received, GET <QueryAPIURL>/healthz
	// to probe.
	QueryAPIURL string
	// ControlAPIURL is the programmable control plane the rest-hook
	// subscriber exposes (POST /program/{tag}, DELETE /program/{tag}).
	// Tests install per-tag response programs (status sequence, header
	// injection, latency, mid-body close) here instead of wiring an
	// in-process http.Handler. Empty for subscribers that don't expose
	// a programmable control plane (e.g. ws subscriber).
	ControlAPIURL string
}

// OP #344: TokenMintHandle was deleted alongside cmd/test-token-mint.
// Adversarial-auth claim-override coverage lives in
// internal/api/auth/verifier_test.go (which generates real RSA keys
// in-process and exercises expired, audience-mismatch, replayed-jti,
// unknown-client, and revoked-client paths against the real verifier).
// Realstack-level tests that need a positive-path bearer token call
// Stack.MintClientToken, which goes through Keycloak's
// client_credentials grant — already covered by Stack.Keycloak +
// provisionKeycloak.

// BinaryHandle is the running cmd/fhir-subs process plus its primary
// HTTP/MLLP listeners.
type BinaryHandle struct {
	// URL is the base URL the binary's API listener is bound to.
	URL string
	// MLLPAddr is host:port of the MLLP listener (empty when MLLP not configured).
	MLLPAddr string
}

// MLLPControlPlaneHandle exposes the real test-mllp-control-plane
// container. The container runs cmd/test-mllp-control-plane and accepts
// POST /scenarios/* JSON requests that synthesize HL7 v2 frames over a
// real TCP socket to the prod binary's MLLP listener (Stack.Binary.MLLPAddr).
//
// Populated only when Boot is called with Options{EnableMLLP: true};
// otherwise zero-valued. The harness brings the container up via the
// "mllp" compose profile after the binary's MLLP port is allocated and
// before Boot returns.
type MLLPControlPlaneHandle struct {
	// HTTPAddr is host:port of the control plane's HTTP listener as
	// resolved from `docker compose port`. Tests dial this address.
	HTTPAddr string
	// URL is the http base URL form: "http://<HTTPAddr>".
	URL string
}

// RateLimit is the operator-facing shape for a per-client token
// bucket the harness installs on the production binary's auth
// middleware. Mirrors cmd/fhir-subs.RateLimitConfig so what callers
// set on Options.* is what the rendered config.yaml writes under
// auth.subscription_create_rate_limit / auth.ws_binding_token_rate_limit.
//
// The H3 LoadDriver (story #258) uses these to drive the binary's
// rate-limit middleware into 429 territory under sustained load.
type RateLimit struct {
	// Burst is the bucket capacity. Zero or negative disables the
	// limit (auth.Middleware treats a nil limiter as pass-through).
	Burst int
	// RefillPerSecond is the steady-state allowed rate. Zero pins
	// the bucket at Burst (strict cap, no replenishment).
	RefillPerSecond float64
	// MaxKeys caps the number of distinct client identities
	// tracked. Zero falls back to the binary's default (65536).
	MaxKeys int
}
