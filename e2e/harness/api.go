// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// stubChannelActivator is the harness's no-op activator for the API's
// channel registry. It always returns HandshakeSucceeded so the API
// flips a freshly-created subscription from `requested` to `active` and
// the submatcher's "active subscriptions" query starts seeing it.
type stubChannelActivator struct{}

func (stubChannelActivator) ActivateSubscription(_ context.Context, _ repos.SubscriptionRow) (handlers.HandshakeOutcome, error) {
	return handlers.HandshakeSucceeded, nil
}

// principalMiddleware injects a fixed auth.Principal so handlers see a
// caller without going through the SMART verifier. The principal
// carries every Subscription scope the handlers check for.
func principalMiddleware(p *auth.Principal) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	}
}

// APIServerConfig parameterizes the API HTTP server the harness stands
// up.
type APIServerConfig struct {
	// Pool is the Postgres pool the pg-backed stores write through.
	Pool *pgxpool.Pool
	// ClientID is the principal id every request appears to come from.
	// The harness ensures an auth_clients row exists for it.
	ClientID string
	// BaseURL and WSBaseURL configure the handler responses; the
	// scheme + host don't have to be reachable, they only affect the
	// CapabilityStatement and $get-ws-binding-token responses.
	BaseURL   string
	WSBaseURL string
	// ExtraChannels supplements the default rest-hook + websocket
	// stubChannelActivator entries. Tests that drive the auth
	// revocation scenario register a 401-returning activator here.
	ExtraChannels handlers.ChannelRegistry
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
}

// APIServer wraps an in-process HTTP server hosting the Subscriptions
// API. Tests pull URL from .URL() and then issue requests via
// http.Client.
type APIServer struct {
	URL    string
	server *http.Server
	ln     net.Listener

	// IsHTTPS is true when the server uses TLS.
	IsHTTPS bool
}

// StartAPIServer brings up an http.Server bound to 127.0.0.1:0 with the
// Subscriptions API routes registered, a stub principal middleware in
// place, and the rest-hook + websocket channels' default stub activator
// installed.
//
// The harness inserts an auth_clients row for cfg.ClientID up front so
// the API's foreign-key check on subscriptions.client_id passes.
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

	if _, err := cfg.Pool.Exec(ctx, `
		INSERT INTO auth_clients (id, scopes, display_name)
		VALUES ($1, ARRAY['system/Subscription.cruds']::text[], $1)
		ON CONFLICT (id) DO NOTHING
	`, cfg.ClientID); err != nil {
		return nil, fmt.Errorf("harness: seed auth_clients: %w", err)
	}

	channels := handlers.ChannelRegistry{
		"rest-hook": stubChannelActivator{},
		"websocket": stubChannelActivator{},
		"email":     stubChannelActivator{},
	}
	for k, v := range cfg.ExtraChannels {
		channels[k] = v
	}

	deps := handlers.Deps{
		Subscriptions:       handlers.NewPgSubscriptionsStore(cfg.Pool),
		Topics:              handlers.NewPgTopicsStore(cfg.Pool),
		Events:              handlers.NewPgEventsStore(cfg.Pool),
		Deliveries:          handlers.NewPgDeliveriesStore(cfg.Pool),
		WsTokens:            handlers.NewPgWsBindingTokensStore(cfg.Pool),
		Audit:               handlers.NewPgAuditStore(cfg.Pool),
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
	}

	r := chi.NewRouter()
	r.Use(principalMiddleware(&auth.Principal{
		ClientID: cfg.ClientID,
		Scopes: []string{
			"system/Subscription.c",
			"system/Subscription.r",
			"system/Subscription.u",
			"system/Subscription.d",
			"system/Subscription.cruds",
		},
		Exp: time.Now().Add(1 * time.Hour),
	}))
	handlers.RegisterRoutes(r, deps)

	if cfg.WSHandler != nil {
		r.Mount(cfg.WSHandlerPath, cfg.WSHandler)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
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
		URL:     fmt.Sprintf("%s://%s", scheme, ln.Addr().String()),
		server:  srv,
		ln:      ln,
		IsHTTPS: cfg.TLSCert != nil,
	}, nil
}

// Close shuts the server down with a 5s grace.
func (s *APIServer) Close() error {
	if s == nil || s.server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.server.Shutdown(ctx)
}

// PostSubscription is a convenience that POSTs a Subscription resource
// JSON via the in-process server, returns the 201 Subscription.id, and
// flips the row to active in the DB so the submatcher's "active
// subscriptions" filter sees it.
//
// The activate goroutine inside createSubscription also flips status,
// but it's async — tests that immediately drive the matcher race
// against it. We mark active synchronously here to remove that race.
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
