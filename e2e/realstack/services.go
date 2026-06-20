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

// PrometheusHandle exposes the real Prometheus container.
type PrometheusHandle struct {
	// Addr is host:port for the Prometheus HTTP API.
	Addr string
	// BaseURL is http://<addr> — used for promtool query instant invocations.
	BaseURL string
}

// OTelHandle exposes the real OTel Collector Contrib container.
type OTelHandle struct {
	// OTLPAddr is the host:port the binary's OTLP exporter dials.
	OTLPAddr string
	// OTLPEndpoint is the full URL form (e.g. http://<addr>) used in the binary's tracing.otlp_endpoint config.
	OTLPEndpoint string
	// SpanFile is the host-side path to the file-exporter pipeline output.
	// Tests read this to assert on captured spans.
	SpanFile string
}

// CoreDNSHandle exposes the real CoreDNS container used for SSRF /
// DNS-rebinding scenarios.
type CoreDNSHandle struct {
	// Addr is host:port of the DNS listener (UDP+TCP).
	Addr string
}

// NginxHandle exposes the real nginx reverse proxy used for
// TLS-terminated topology tests.
type NginxHandle struct {
	// Addr is host:port for the proxy frontend.
	Addr string
}

// MitmproxyHandle exposes the real mitmproxy container used for
// strip-STARTTLS scenarios.
type MitmproxyHandle struct {
	// Addr is host:port of the mitmproxy listener.
	Addr string
}

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
}

// TokenMintHandle exposes the real cmd/test-token-mint binary that runs
// inside docker-compose. Adversarial-auth tests use it to mint RS256
// JWTs with arbitrary claim overrides without round-tripping through
// Keycloak. The harness wires its JWKS URL into an auth_clients row so
// the prod fhir-subs verifier trusts tokens it signs.
type TokenMintHandle struct {
	// Addr is host:port the test process can reach the binary on.
	Addr string
	// TokenAPIURL is the http base URL the test process POSTs /mint
	// requests to (e.g. http://<addr>). Stack.MintTestToken wraps it.
	TokenAPIURL string
	// JWKSURL is the full JWKS endpoint (http://<addr>/jwks.json) the
	// prod binary's verifier fetches via the per-client lookup.
	JWKSURL string
	// Issuer is the iss claim the binary stamps on default-minted
	// tokens. Distinct from Keycloak.IssuerURL.
	Issuer string
	// ClientID is the auth_clients id the harness provisioned and
	// pointed at this binary's JWKS URL. Default-minted tokens carry
	// this value as client_id and sub so the verifier can resolve.
	ClientID string
}

// BinaryHandle is the running cmd/fhir-subs process plus its primary
// HTTP/MLLP listeners.
type BinaryHandle struct {
	// URL is the base URL the binary's API listener is bound to.
	URL string
	// MLLPAddr is host:port of the MLLP listener (empty when MLLP not configured).
	MLLPAddr string
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
