// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
)

// harnessAudience is the aud claim every harness-minted token carries and
// the audience the harness verifier validates against. Tests that need to
// drive an audience-mismatch path mint their own tokens.
const harnessAudience = "fhir-subs-e2e"

// APIServerConfig parameterizes the API HTTP server the harness stands
// up.
type APIServerConfig struct {
	// Pool is the Postgres pool the pg-backed stores write through.
	Pool *pgxpool.Pool
	// ClientID is the principal id every request appears to come from.
	// The harness ensures an auth_clients row exists for it pointing at
	// the harness JWKS.
	ClientID string
	// BaseURL and WSBaseURL configure the handler responses; the
	// scheme + host don't have to be reachable, they only affect the
	// CapabilityStatement and $get-ws-binding-token responses.
	BaseURL   string
	WSBaseURL string
	// ExtraChannels overrides or supplements the default rest-hook,
	// websocket, and email entries built from the *ProbeURL / SMTP*
	// fields below. Tests that drive the auth revocation scenario
	// register a 401-returning activator here. Keys present in
	// ExtraChannels win over the defaults.
	ExtraChannels handlers.ChannelRegistry

	// RestHookProbeURL is the localhost test subscriber the rest-hook
	// activator POSTs the FHIR R5 handshake Bundle to. Required when
	// ExtraChannels does not override "rest-hook". OP #147 forbids the
	// no-op stubChannelActivator that previously stood in here.
	RestHookProbeURL string

	// SMTPHost / SMTPPort point the email activator's RCPT-TO probe at
	// a localhost SMTP fake. Required when ExtraChannels does not
	// override "email". OP #147 forbids the no-op stub.
	SMTPHost string
	SMTPPort int

	// WebsocketProbeURL is the ws:// URL of the harness's WS upgrade
	// handler. Required when ExtraChannels does not override
	// "websocket". OP #147 forbids the no-op stub.
	WebsocketProbeURL string
	// TLSCert, when non-nil, makes the server listen with TLS. The
	// rest-hook channel demands HTTPS, so production-shaped scenarios
	// always use this; the WSS scenario also needs TLS to talk to the
	// WS endpoint.
	TLSCert *tls.Certificate
	// WSHandler is mounted at HandlerPath when non-nil. The websocket
	// channel exposes such a handler; e2e WSS scenarios pass it here
	// so the same listener serves both the API and the upgrade route.
	WSHandler http.Handler
	// WSHandlerPath is the route at which to mount WSHandler. Empty
	// uses /ws/subscriptions (the channel's HandlerPath constant).
	WSHandlerPath string

	// URLValidator is the optional SSRF guard the API runs against
	// channel.endpoint at create / update time (B-11). Tests that
	// exercise the SSRF path inject a configured validator; tests that
	// don't care leave this nil and the handler skips the check.
	URLValidator handlers.URLValidator

	// LifecycleCtx, ActivationTimeout, ActivationWaitGroup plumb
	// through to handlers.Deps so e2e tests can join in-flight
	// activation goroutines (B-10).
	LifecycleCtx        context.Context
	ActivationTimeout   time.Duration
	ActivationWaitGroup *sync.WaitGroup

	// AuditMaxBytes plumbs through to handlers.Deps.AuditMaxBytes for
	// B-13 redaction tests.
	AuditMaxBytes int

	// Metrics is the optional MetricsRecorder e2e tests inject to
	// observe RecordActivatePanic counter increments (B-10).
	Metrics handlers.MetricsRecorder

	// SubscriptionCreateRateLimit and WSBindingTokenRateLimit plumb the
	// per-client token-bucket limiters through to handlers.Deps so e2e
	// tests can drive the S-3.3 429 path without standing up a parallel
	// chi router.
	SubscriptionCreateRateLimit *auth.ClientRateLimiter
	WSBindingTokenRateLimit     *auth.ClientRateLimiter

	// ClientScopes overrides the default scope set granted to ClientID
	// in auth_clients and stamped onto harness-minted tokens. Empty
	// keeps the harness's full Subscription.cruds default.
	ClientScopes []string
}

// APIServer wraps an in-process HTTP server hosting the Subscriptions
// API. Tests pull URL from .URL() and then issue requests via
// http.Client. The server runs the real auth.Verifier middleware against
// a per-server JWKS — Bearer() and Client() are the canonical paths to
// authenticate against it (OP #146 banned the fixed-principal stub).
type APIServer struct {
	URL    string
	server *http.Server
	ln     net.Listener

	// IsHTTPS is true when the server uses TLS.
	IsHTTPS bool

	// jwksSrv is the per-server JWKS endpoint the auth_clients row points
	// at. Closed in Close().
	jwksSrv *httptest.Server

	// restHookSrv is the localhost rest-hook probe sink the harness spins
	// up when cfg.RestHookProbeURL is empty (OP #327). Without it the
	// rest-hook channel falls through to failClosedActivator and every
	// scenario that posts a Subscription lands in status=error. Closed in
	// Close().
	restHookSrv *httptest.Server

	// signer is the RSA key the harness uses to mint bearer tokens.
	signer *harnessSigner

	// clientID is the principal id the harness-minted tokens carry as
	// client_id/sub.
	clientID string

	// scopes is the scope string harness-minted tokens carry.
	scopes []string
}

// harnessSigner is a small RSA-key + kid pair used to mint test
// JWTs and serve a JWKS document.
type harnessSigner struct {
	priv *rsa.PrivateKey
	kid  string
}

func newHarnessSigner() (*harnessSigner, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("harness: rsa keygen: %w", err)
	}
	return &harnessSigner{priv: priv, kid: uuid.NewString()}, nil
}

func (s *harnessSigner) jwks() map[string]any {
	pub := s.priv.PublicKey
	enc := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	return map[string]any{
		"keys": []any{
			map[string]any{
				"kty": "RSA",
				"alg": "RS256",
				"use": "sig",
				"kid": s.kid,
				"n":   enc(pub.N.Bytes()),
				"e":   enc([]byte{1, 0, 1}),
			},
		},
	}
}

// StartAPIServer brings up an http.Server bound to 127.0.0.1:0 with the
// Subscriptions API routes registered, the real auth.Verifier
// middleware installed against a per-server JWKS, and the rest-hook +
// websocket + email channels each backed by a real activator
// (cfg.RestHookProbeURL / cfg.SMTPHost+SMTPPort / cfg.WebsocketProbeURL).
// When a probe target is empty the slot is filled with a fail-closed
// activator that returns HandshakeFailed — never the synthetic
// "succeeded" stub OP #147 banned.
//
// The harness inserts an auth_clients row for cfg.ClientID up front,
// pointing at the harness's JWKS so the verifier can validate
// harness-minted tokens via the real RSA keyfunc path. OP #146 banned
// the fixed-principal middleware that previously bypassed the verifier.
func StartAPIServer(ctx context.Context, cfg APIServerConfig) (*APIServer, error) {
	if cfg.Pool == nil {
		return nil, errors.New("harness: nil pool")
	}
	if cfg.ClientID == "" {
		return nil, errors.New("harness: empty ClientID")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://127.0.0.1"
	}
	if cfg.WSBaseURL == "" {
		cfg.WSBaseURL = "wss://127.0.0.1/ws"
	}
	if cfg.WSHandlerPath == "" {
		cfg.WSHandlerPath = "/ws/subscriptions"
	}
	scopes := cfg.ClientScopes
	if len(scopes) == 0 {
		scopes = []string{
			"system/Subscription.cruds",
			"system/Subscription.r",
			"system/Subscription.s",
			"system/Subscription.c",
			"system/Subscription.u",
			"system/Subscription.d",
		}
	}

	signer, err := newHarnessSigner()
	if err != nil {
		return nil, err
	}

	jwksMux := http.NewServeMux()
	jwksMux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(signer.jwks())
	})
	jwksSrv := httptest.NewServer(jwksMux)

	if _, seedErr := cfg.Pool.Exec(ctx, `
		INSERT INTO auth_clients (id, scopes, display_name, jwks_url)
		VALUES ($1, $2::text[], $1, $3)
		ON CONFLICT (id) DO UPDATE SET
			scopes = EXCLUDED.scopes,
			jwks_url = EXCLUDED.jwks_url
	`, cfg.ClientID, scopes, jwksSrv.URL+"/jwks"); seedErr != nil {
		jwksSrv.Close()
		return nil, fmt.Errorf("harness: seed auth_clients: %w", seedErr)
	}

	// OP #327: when callers don't pass a rest-hook probe URL, spin up a
	// localhost 200-OK sink so the rest-hook activator handshakes
	// successfully. The previous behavior (failClosedActivator) made
	// every scenario that posts a Subscription land in status=error
	// "harness: rest-hook channel has no probe target configured".
	var restHookSrv *httptest.Server
	if cfg.RestHookProbeURL == "" && cfg.ExtraChannels["rest-hook"] == nil {
		restHookSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		cfg.RestHookProbeURL = restHookSrv.URL
	}
	closeAux := func() {
		if restHookSrv != nil {
			restHookSrv.Close()
		}
		jwksSrv.Close()
	}

	channels, err := buildHarnessChannels(cfg)
	if err != nil {
		closeAux()
		return nil, err
	}

	auditStore, err := audit.NewPgStore(cfg.Pool, audit.PgStoreOptions{})
	if err != nil {
		closeAux()
		return nil, fmt.Errorf("harness: audit store: %w", err)
	}
	auditWriter, err := audit.NewWriter(audit.WriterOptions{
		Store: auditStore,
		Sink:  audit.NewStdoutSink(),
		Clock: time.Now,
	})
	if err != nil {
		closeAux()
		return nil, fmt.Errorf("harness: audit writer: %w", err)
	}

	verif, err := auth.NewVerifier(auth.VerifierConfig{
		Audience:          harnessAudience,
		ClientLookup:      handlers.NewAuthClientLookup(cfg.Pool),
		AllowInsecureJWKS: true,
	})
	if err != nil {
		closeAux()
		return nil, fmt.Errorf("harness: NewVerifier: %w", err)
	}

	deps := handlers.Deps{
		Auth:                verif.Middleware,
		Subscriptions:       handlers.NewPgSubscriptionsStore(cfg.Pool),
		Topics:              handlers.NewPgTopicsStore(cfg.Pool),
		Events:              handlers.NewPgEventsStore(cfg.Pool),
		Deliveries:          handlers.NewPgDeliveriesStore(cfg.Pool),
		WsTokens:            handlers.NewPgWsBindingTokensStore(cfg.Pool),
		Audit:               handlers.NewChainedAuditStore(auditWriter),
		Channels:            channels,
		Now:                 func() time.Time { return time.Now().UTC() },
		WSBindingTTL:        5 * time.Minute,
		BaseURL:             cfg.BaseURL,
		WSBaseURL:           cfg.WSBaseURL,
		ServerVersion:       "harness",
		URLValidator:        cfg.URLValidator,
		LifecycleCtx:        cfg.LifecycleCtx,
		ActivationTimeout:   cfg.ActivationTimeout,
		ActivationWaitGroup: cfg.ActivationWaitGroup,
		AuditMaxBytes:       cfg.AuditMaxBytes,
		Metrics:             cfg.Metrics,

		SubscriptionCreateRateLimit: cfg.SubscriptionCreateRateLimit,
		WSBindingTokenRateLimit:     cfg.WSBindingTokenRateLimit,
	}

	r := chi.NewRouter()
	handlers.RegisterPublicRoutes(r, deps)
	handlers.RegisterRoutes(r, deps)

	if cfg.WSHandler != nil {
		r.Mount(cfg.WSHandlerPath, cfg.WSHandler)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		closeAux()
		return nil, fmt.Errorf("harness: listen: %w", err)
	}

	srv := &http.Server{
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	scheme := "http"
	if cfg.TLSCert != nil {
		srv.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{*cfg.TLSCert},
			MinVersion:   tls.VersionTLS12,
		}
		scheme = "https"
	}

	go func() {
		if cfg.TLSCert != nil {
			_ = srv.ServeTLS(ln, "", "")
		} else {
			_ = srv.Serve(ln)
		}
	}()

	return &APIServer{
		URL:         fmt.Sprintf("%s://%s", scheme, ln.Addr().String()),
		server:      srv,
		ln:          ln,
		IsHTTPS:     cfg.TLSCert != nil,
		jwksSrv:     jwksSrv,
		restHookSrv: restHookSrv,
		signer:      signer,
		clientID:    cfg.ClientID,
		scopes:      scopes,
	}, nil
}

// Close shuts the server down with a 5s grace.
func (s *APIServer) Close() error {
	if s == nil || s.server == nil {
		return nil
	}
	if s.jwksSrv != nil {
		s.jwksSrv.Close()
	}
	if s.restHookSrv != nil {
		s.restHookSrv.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.server.Shutdown(ctx)
}

// Bearer returns a freshly-minted Bearer token authorised for the
// harness ClientID with the harness scope set. Each call gets a unique
// jti, so callers can issue many requests in a single test without
// tripping the verifier's replay cache.
func (s *APIServer) Bearer() string {
	now := time.Now().UTC()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss":       s.clientID,
		"sub":       s.clientID,
		"aud":       harnessAudience,
		"client_id": s.clientID,
		"jti":       uuid.NewString(),
		"iat":       now.Add(-1 * time.Minute).Unix(),
		"exp":       now.Add(15 * time.Minute).Unix(),
		"scope":     strings.Join(s.scopes, " "),
	})
	tok.Header["kid"] = s.signer.kid
	signed, err := tok.SignedString(s.signer.priv)
	if err != nil {
		panic(fmt.Sprintf("harness: sign bearer: %v", err))
	}
	return signed
}

// MintBearer mints a token with the supplied claim overrides applied on
// top of the harness defaults. Use it for tests that exercise the
// verifier's failure modes (expired exp, audience mismatch, replayed
// jti, missing scopes). For the happy path, prefer Bearer().
func (s *APIServer) MintBearer(overrides map[string]any) string {
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"iss":       s.clientID,
		"sub":       s.clientID,
		"aud":       harnessAudience,
		"client_id": s.clientID,
		"jti":       uuid.NewString(),
		"iat":       now.Add(-1 * time.Minute).Unix(),
		"exp":       now.Add(15 * time.Minute).Unix(),
		"scope":     strings.Join(s.scopes, " "),
	}
	for k, v := range overrides {
		claims[k] = v
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = s.signer.kid
	signed, err := tok.SignedString(s.signer.priv)
	if err != nil {
		panic(fmt.Sprintf("harness: sign bearer: %v", err))
	}
	return signed
}

// Client returns an http.Client whose transport injects an
// Authorization: Bearer header carrying a freshly-minted harness token
// on every request. Test callers that previously used http.DefaultClient
// against the fixed-principal harness now use this client.
func (s *APIServer) Client() *http.Client {
	return &http.Client{
		Transport: &bearerTransport{server: s, base: http.DefaultTransport},
		Timeout:   30 * time.Second,
	}
}

// ClientID returns the principal id every harness-minted token carries.
func (s *APIServer) ClientID() string { return s.clientID }

// JWKSURL returns the URL of the per-server JWKS endpoint. Tests that
// drive the verifier via a real-stack prod binary point that binary's
// auth.jwks_url config at this URL.
func (s *APIServer) JWKSURL() string {
	if s.jwksSrv == nil {
		return ""
	}
	return s.jwksSrv.URL + "/jwks"
}

// bearerTransport adds Authorization: Bearer <fresh-token> to every
// outgoing request that doesn't already carry an Authorization header.
type bearerTransport struct {
	server *APIServer
	base   http.RoundTripper
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Authorization") == "" {
		// Clone to avoid mutating the caller's request when the
		// transport is reused.
		clone := req.Clone(req.Context())
		clone.Header.Set("Authorization", "Bearer "+t.server.Bearer())
		return t.base.RoundTrip(clone)
	}
	return t.base.RoundTrip(req)
}

// PostSubscription is a convenience that POSTs a Subscription resource
// JSON via the in-process server, returns the 201 Subscription.id, and
// flips the row to active in the DB so the submatcher's "active
// subscriptions" filter sees it.
//
// The activate goroutine inside createSubscription also flips status,
// but it's async — tests that immediately drive the matcher race
// against it. We mark active synchronously here to remove that race.
//
// Callers should pass srv.Client() so the request carries a real
// Bearer token; passing http.DefaultClient yields a 401 from the real
// verifier.
func PostSubscription(ctx context.Context, srv *APIServer, client *http.Client, body []byte) (uuid.UUID, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/Subscription/", strings.NewReader(string(body)))
	if err != nil {
		return uuid.Nil, err
	}
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := client.Do(req)
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		return uuid.Nil, fmt.Errorf("POST /Subscription: status %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	idStr := strings.TrimPrefix(loc, "/Subscription/")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return uuid.Nil, fmt.Errorf("parse subscription id from Location %q: %w", loc, err)
	}
	return id, nil
}

// MarkSubscriptionActive flips a subscription's status to 'active'
// directly. The API's createSubscription handler does this in a
// background goroutine after the handshake succeeds, but tests need a
// synchronous version to avoid a race with the submatcher's claim
// loop.
func MarkSubscriptionActive(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) error {
	const sql = `UPDATE subscriptions SET status = 'active', updated_at = now() WHERE id = $1`
	_, err := pool.Exec(ctx, sql, id)
	return err
}
