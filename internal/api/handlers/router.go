// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/fhirerror"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// DefaultActivationTimeout bounds the per-subscription activation
// handshake when Deps.ActivationTimeout is unset.
const DefaultActivationTimeout = 30 * time.Second

// Middleware is the chi-compatible per-handler middleware shape
// (chi.Middlewares' element type). Naming the slot in Deps as a typed
// value — instead of a bare func(http.Handler) http.Handler — gives the
// auth wiring a single, greppable touchpoint and prevents a future
// caller from mounting RegisterRoutes without an auth gate (N-1.4).
type Middleware = func(http.Handler) http.Handler

// HandshakeOutcome is the result of a channel module's per-subscription
// activation handshake.
type HandshakeOutcome string

// HandshakeOutcome values.
const (
	HandshakeSucceeded HandshakeOutcome = "succeeded"
	HandshakeFailed    HandshakeOutcome = "failed"
)

// ChannelActivator is the narrow interface the API needs from a channel
// module. The full Channel SPI is owned by the channels package; the
// API touches only on_subscription_activated.
type ChannelActivator interface {
	ActivateSubscription(ctx context.Context, sub repos.SubscriptionRow) (HandshakeOutcome, error)
}

// ChannelRegistry maps channel-type code (e.g., "rest-hook",
// "websocket") to its activator.
//
// The registry is constructed once at startup, frozen, and read across
// goroutines without a mutex. Mutating it after RegisterRoutes returns
// is undefined and will race; the runtime treats it as immutable
// (S-2.5).
type ChannelRegistry map[string]ChannelActivator

// MetricsRecorder is the narrow surface handlers use to record
// subscription, ws-binding-token, and validation metrics. The API
// metrics package provides the canonical implementation; tests can
// inject an in-memory recorder. Nil is permitted everywhere.
type MetricsRecorder interface {
	RecordSubscriptionCreated()
	RecordSubscriptionUpdated()
	RecordSubscriptionDeleted()
	RecordWSBindingTokenIssued()
	RecordValidationFailure(kind string)
}

// ActivatePanicRecorder is an optional extension implemented by
// MetricsRecorder to count recovered panics in fire-and-forget
// activation goroutines (B-10). The handlers detect this via a type
// assertion so existing recorders continue to compile without change.
type ActivatePanicRecorder interface {
	RecordActivatePanic()
}

// RandFailureRecorder is an optional extension implemented by
// MetricsRecorder to count crypto/rand.Read failures in token-mint
// paths. The previous code only tripped the generic HTTP 500 counter,
// which buried a kernel entropy failure under unrelated 500s (N-1).
// Detected via type assertion so legacy recorders compile unchanged.
type RandFailureRecorder interface {
	RecordRandFailure()
}

// Deps is the bundle of dependencies the handlers need at request time.
type Deps struct {
	// Auth is the per-handler middleware that authenticates every
	// authenticated route — including the catch-all NotFound and
	// MethodNotAllowed responders. RegisterRoutes installs it on the
	// route group itself so a future caller cannot accidentally mount
	// the routes without an auth gate (N-1.4). Must be non-nil;
	// RegisterRoutes panics otherwise so misconfiguration is caught at
	// startup, not at the first unauthenticated request.
	Auth Middleware

	Subscriptions SubscriptionsStore
	Topics        SubscriptionTopicsStore
	Events        EhrEventsStore
	Deliveries    DeliveriesStore
	WsTokens      WsBindingTokensStore
	Audit         AuditStore
	Channels      ChannelRegistry

	// Metrics is the optional recorder used to emit per-action
	// subscription / ws / validation metrics. Nil is fine.
	Metrics MetricsRecorder

	// Now returns the current time. Tests substitute a fixed value.
	Now func() time.Time

	// WSBindingTTL is the lifetime of issued ws-binding tokens.
	WSBindingTTL time.Duration

	// BaseURL is the public base URL of this server. Used to build
	// CapabilityStatement and absolute URLs.
	BaseURL string

	// WSBaseURL is the public WSS URL prefix used in
	// $get-ws-binding-token responses (e.g., "wss://api.example/ws").
	WSBaseURL string

	// ServerVersion is rendered into CapabilityStatement.software.
	ServerVersion string

	// LifecycleCtx is the long-lived server context. Activation
	// goroutines derive their per-call ctx from it so that server
	// shutdown propagates cancellation to in-flight handshakes (B-10).
	// Nil is treated as context.Background.
	LifecycleCtx context.Context

	// ActivationTimeout bounds the per-subscription activation
	// handshake. A slow vendor cannot pin a goroutine + DB conn forever
	// (B-10). Zero means use DefaultActivationTimeout.
	ActivationTimeout time.Duration

	// ActivationWaitGroup, when non-nil, is incremented for every
	// in-flight activation goroutine. The lifecycle module joins on it
	// during graceful shutdown so handshakes either finish, time out,
	// or are canceled before the process exits (B-10).
	ActivationWaitGroup *sync.WaitGroup

	// URLValidator, when non-nil, vets the subscriber-supplied
	// channel.endpoint URL on every create and update before the row
	// is persisted (B-11). A nil validator is treated as "no policy"
	// for backward-compatibility with existing tests; production
	// wiring MUST set this.
	URLValidator URLValidator

	// AuditMaxBytes caps the canonical request body persisted to
	// audit_log on create / update (B-13). Zero means
	// DefaultAuditMaxBytes (16 KiB). All requests are also passed
	// through RedactSubscriptionForAudit before truncation so that
	// channel.header[] and similar secret-bearing fields never reach
	// disk in plaintext.
	AuditMaxBytes int

	// MaxBodyBytes caps the request body the create / update handlers
	// will read. Zero means DefaultMaxBodyBytes. (S-2.2)
	MaxBodyBytes int64

	// FHIRVersion is rendered into CapabilityStatement.fhirVersion.
	// Empty means DefaultFHIRVersion. (S-2.13)
	FHIRVersion string

	// MaxStatusBulkIDs caps the number of `id` query parameters
	// accepted by GET /Subscription/$status. Zero means
	// DefaultMaxStatusBulkIDs. (S-2.11)
	MaxStatusBulkIDs int

	// MaxSchemaErrorBytes caps OperationOutcome.diagnostics for
	// JSON-schema validation errors. Zero means
	// DefaultMaxSchemaErrorBytes. (S-2.3)
	MaxSchemaErrorBytes int

	// Logger is the structured logger used for activate-side errors
	// that the API can't surface to the client (S-2.7). Nil is
	// permitted; activate calls then drop the error silently — that's
	// the legacy behavior, kept for backwards-compat.
	Logger *slog.Logger

	// TokenEndpointURL, when non-empty, is rendered into the
	// CapabilityStatement's SMART-on-FHIR security extension (P1.7).
	// Empty means the SMART extension lists only the security service
	// without absolute URLs.
	TokenEndpointURL string

	// JWKSURL is the absolute URL of the server's JWKS document. Wiring
	// usually sets this to {BaseURL}/jwks.json. Rendered into the
	// CapabilityStatement (P1.7).
	JWKSURL string

	// SupportedFHIRVersions lists every FHIR version the server can
	// negotiate via the Accept header. Defaults to [DefaultFHIRVersion].
	// Rendered into a versionshim extension on the CapabilityStatement
	// so subscribers can discover the negotiable set without speaking
	// the wire (P1.7).
	SupportedFHIRVersions []string

	// AdminToken, when non-empty, enables the read-only admin operator
	// surface mounted at AdminPathPrefix (default `/admin`). The token
	// is the only auth gate; network-level scoping is the operator's
	// responsibility (P1.6). Empty means the admin surface is disabled
	// at the router layer — the routes do not exist.
	AdminToken string

	// AdminPathPrefix overrides the default `/admin` mount point. Empty
	// means DefaultAdminPathPrefix.
	AdminPathPrefix string

	// DeadLetters is the read-only dead-letters list adapter the admin
	// surface needs. Nil disables /admin/dead_letters (returns 503).
	DeadLetters DeadLettersListStore

	// SupervisorStatus, when non-nil, enables GET /admin/supervisor/status
	// — the operator-facing snapshot of every supervised adapter worker
	// (HL7 processor, matcher, submatcher, scheduler). Nil disables the
	// route entirely so a probe does not learn about a stack that is not
	// present (story #99).
	SupervisorStatus SupervisorStatusReader

	// SearchPageSize is the default page size for GET /Subscription
	// when the client does not pass `_count`. Zero means
	// DefaultSearchPageSize. (S-2.8)
	SearchPageSize int

	// SearchMaxPageSize caps the user-supplied `_count`. Zero means
	// DefaultSearchMaxPageSize. (S-2.8)
	SearchMaxPageSize int

	// EventReplayPageSize is the maximum number of events returned per
	// $events response. Replaces the legacy hardcoded LIMIT 1000.
	// Truncation is signaled to the client via a Bundle.link `next`
	// relation pointing at the next eventsSinceNumber. Zero means
	// DefaultEventReplayPageSize. (S-2.15)
	EventReplayPageSize int

	// SubscriptionCreateRateLimit, when non-nil, gates POST /Subscription
	// behind a per-client token bucket so a single rogue client cannot
	// starve others. Nil disables rate limiting on this endpoint
	// (legacy behavior). (S-3.3)
	SubscriptionCreateRateLimit *auth.ClientRateLimiter

	// WSBindingTokenRateLimit, when non-nil, gates the
	// $get-ws-binding-token operation behind a per-client token bucket.
	// Nil disables rate limiting on this endpoint. (S-3.3)
	WSBindingTokenRateLimit *auth.ClientRateLimiter

	// AdminRateLimit, when non-nil, gates every /admin/* request behind a
	// per-token bucket so a runaway operator script (or a credential-stuffing
	// probe that has guessed the shared secret) cannot pin the admin
	// surface or amplify the audit-log write rate. Nil disables rate
	// limiting on the admin surface (story #92).
	AdminRateLimit *auth.ClientRateLimiter
}

// DefaultMaxBodyBytes is the default request-body cap.
const DefaultMaxBodyBytes int64 = 1 << 20 // 1 MiB

// DefaultFHIRVersion is the default CapabilityStatement.fhirVersion.
const DefaultFHIRVersion = "5.0.0"

// DefaultMaxStatusBulkIDs caps GET /Subscription/$status?id=... fan-out.
const DefaultMaxStatusBulkIDs = 256

// DefaultMaxSchemaErrorBytes caps schema-validation diagnostics length.
const DefaultMaxSchemaErrorBytes = 1024

// DefaultSearchPageSize is the default page size for GET /Subscription
// when the client passes no `_count`. (S-2.8)
const DefaultSearchPageSize = 50

// DefaultSearchMaxPageSize caps `_count` on GET /Subscription. (S-2.8)
const DefaultSearchMaxPageSize = 200

// DefaultEventReplayPageSize replaces the legacy hardcoded LIMIT 1000
// in $events replay; the client gets a Bundle.link `next` whenever the
// underlying log has more rows than this. (S-2.15)
const DefaultEventReplayPageSize = 1000

// instantFormat is the canonical FHIR `instant` rendering with
// millisecond precision and a `Z` suffix (not `+00:00`). (S-2.9)
const instantFormat = "2006-01-02T15:04:05.000Z"

// RegisterRoutes wires every handler onto r. The auth middleware is
// installed on the route group itself (via Deps.Auth, a chi-compatible
// Middleware) so unauthenticated callers hit 401 — including on the
// catch-all NotFound and MethodNotAllowed paths. RegisterRoutes panics
// when Deps.Auth is nil so wiring mistakes fail loud at startup
// (N-1.4).
func RegisterRoutes(r chi.Router, d Deps) {
	if d.Auth == nil {
		panic("handlers.RegisterRoutes: Deps.Auth is nil — auth middleware is required (N-1.4)")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.WSBindingTTL == 0 {
		d.WSBindingTTL = 5 * time.Minute
	}
	if d.LifecycleCtx == nil {
		d.LifecycleCtx = context.Background()
	}
	if d.ActivationTimeout == 0 {
		d.ActivationTimeout = DefaultActivationTimeout
	}
	if d.MaxBodyBytes <= 0 {
		d.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if d.FHIRVersion == "" {
		d.FHIRVersion = DefaultFHIRVersion
	}
	if d.MaxStatusBulkIDs <= 0 {
		d.MaxStatusBulkIDs = DefaultMaxStatusBulkIDs
	}
	if d.MaxSchemaErrorBytes <= 0 {
		d.MaxSchemaErrorBytes = DefaultMaxSchemaErrorBytes
	}
	if d.SearchPageSize <= 0 {
		d.SearchPageSize = DefaultSearchPageSize
	}
	if d.SearchMaxPageSize <= 0 {
		d.SearchMaxPageSize = DefaultSearchMaxPageSize
	}
	if d.EventReplayPageSize <= 0 {
		d.EventReplayPageSize = DefaultEventReplayPageSize
	}

	h := &server{deps: d}

	// Group binds Auth to the same sub-mux that owns NotFound and
	// MethodNotAllowed, so a request that misses every route still runs
	// through the auth middleware before reaching the catch-all (N-1.4).
	r.Group(func(r chi.Router) {
		r.Use(d.Auth)

		r.Route("/Subscription", func(r chi.Router) {
			r.With(d.SubscriptionCreateRateLimit.Middleware()).Post("/", h.createSubscription)
			r.Get("/", h.searchSubscriptions)
			r.Get("/{id}", h.readSubscription)
			r.Put("/{id}", h.updateSubscription)
			r.Delete("/{id}", h.deleteSubscription)
			r.Get("/{id}/$status", h.opStatusSingle)
			r.Get("/$status", h.opStatusBulk)
			r.Get("/{id}/$events", h.opEvents)
			r.With(d.WSBindingTokenRateLimit.Middleware()).Post("/{id}/$get-ws-binding-token", h.opGetWsBindingToken)
		})

		r.Get("/SubscriptionTopic", h.searchTopics)
		r.Get("/SubscriptionTopic/{id}", h.readTopic)
		r.Get("/metadata", h.getCapabilityStatement)

		// Catch-all: every unknown route returns an OperationOutcome 404.
		r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
			fhirerror.WriteError(w, http.StatusNotFound, fhirerror.CodeNotFound, "no such endpoint")
		})
		r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
			fhirerror.WriteError(w, http.StatusMethodNotAllowed, fhirerror.CodeNotSupported, "method not allowed")
		})
	})
}

// RegisterPublicRoutes wires the routes that MUST NOT be wrapped in
// auth middleware: the FHIR `/metadata` (CapabilityStatement) endpoint
// is required by FHIR conformance probes and is fetched
// unauthenticated. Production wiring mounts this on the bare server
// mux so the auth middleware never sees the request (S-2.1).
func RegisterPublicRoutes(r chi.Router, d Deps) {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.FHIRVersion == "" {
		d.FHIRVersion = DefaultFHIRVersion
	}
	h := &server{deps: d}
	r.Get("/metadata", h.getCapabilityStatementPublic)
}

type server struct {
	deps Deps
}
