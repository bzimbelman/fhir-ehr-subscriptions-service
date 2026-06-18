// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package metrics owns the Prometheus instrumentation for the
// Subscriptions API surface. Per LLD §9 and ADR 0008 #10 every metric
// emitted by the API carries the `fhir_subs_api_` prefix.
//
// Metrics:
//
//   - fhir_subs_api_requests_total{method,route,status}
//   - fhir_subs_api_request_duration_seconds{method,route}
//   - fhir_subs_api_auth_failures_total{reason}
//   - fhir_subs_api_subscription_{created,updated,deleted}_total
//   - fhir_subs_api_validation_failures_total{kind}
//   - fhir_subs_api_token_issued_total
//   - fhir_subs_api_ws_binding_token_issued_total
//
// The middleware captures the chi-resolved route pattern so that
// `route` cardinality stays bounded (one series per registered route,
// not one per `{id}`).
package metrics

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics is the bundle the API package wires through to handlers and
// middleware. A nil *Metrics is permitted on every method as a no-op.
type Metrics struct {
	RequestsTotal        *prometheus.CounterVec
	RequestDuration      *prometheus.HistogramVec
	AuthFailures         *prometheus.CounterVec
	SubscriptionCreated  prometheus.Counter
	SubscriptionUpdated  prometheus.Counter
	SubscriptionDeleted  prometheus.Counter
	ValidationFailures   *prometheus.CounterVec
	TokenIssued          prometheus.Counter
	WSBindingTokenIssued prometheus.Counter
	ActivatePanicTotal   prometheus.Counter
	RandFailuresTotal    prometheus.Counter
}

// New constructs the API metric set and registers it with reg. If reg
// is nil, a fresh registry is used (handy for tests).
func New(reg prometheus.Registerer) (*Metrics, error) {
	if reg == nil {
		reg = prometheus.NewRegistry()
	}

	m := &Metrics{
		RequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "fhir_subs_api_requests_total",
			Help: "Total HTTP requests served by the Subscriptions API.",
		}, []string{"method", "route", "status"}),
		RequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "fhir_subs_api_request_duration_seconds",
			Help:    "HTTP request latency for the Subscriptions API.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
		AuthFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "fhir_subs_api_auth_failures_total",
			Help: "Authentication failures, labeled by failure reason.",
		}, []string{"reason"}),
		SubscriptionCreated: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "fhir_subs_api_subscription_created_total",
			Help: "Subscriptions successfully created via the API.",
		}),
		SubscriptionUpdated: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "fhir_subs_api_subscription_updated_total",
			Help: "Subscriptions successfully updated via the API.",
		}),
		SubscriptionDeleted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "fhir_subs_api_subscription_deleted_total",
			Help: "Subscriptions successfully deleted via the API.",
		}),
		ValidationFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "fhir_subs_api_validation_failures_total",
			Help: "Request validation failures, labeled by kind (schema|semantic).",
		}, []string{"kind"}),
		TokenIssued: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "fhir_subs_api_token_issued_total",
			Help: "Access tokens issued by the OAuth2 token endpoint.",
		}),
		WSBindingTokenIssued: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "fhir_subs_api_ws_binding_token_issued_total",
			Help: "WebSocket binding tokens issued by $get-ws-binding-token.",
		}),
		ActivatePanicTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "fhir_subs_api_activate_panic_total",
			Help: "Recovered panics in the fire-and-forget activation goroutines (B-10).",
		}),
		RandFailuresTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "fhir_subs_api_rand_failures_total",
			Help: "crypto/rand.Read failures in token-mint paths (N-1).",
		}),
	}

	collectors := []prometheus.Collector{
		m.RequestsTotal,
		m.RequestDuration,
		m.AuthFailures,
		m.SubscriptionCreated,
		m.SubscriptionUpdated,
		m.SubscriptionDeleted,
		m.ValidationFailures,
		m.TokenIssued,
		m.WSBindingTokenIssued,
		m.ActivatePanicTotal,
		m.RandFailuresTotal,
	}

	for _, c := range collectors {
		if err := reg.Register(c); err != nil {
			var are prometheus.AlreadyRegisteredError
			if errors.As(err, &are) {
				continue
			}
			return nil, fmt.Errorf("metrics: register collector: %w", err)
		}
	}

	// Pre-initialize one zero-valued time series per labeled vector so
	// the metric family appears in /metrics scrape output and in
	// registry.Gather() before the first event lands. This makes
	// "absent_over_time" alert rules behave correctly during cold start.
	m.AuthFailures.WithLabelValues("malformed").Add(0)
	m.ValidationFailures.WithLabelValues("schema").Add(0)
	m.RequestsTotal.WithLabelValues("GET", "/metadata", "200").Add(0)
	m.RequestDuration.WithLabelValues("GET", "/metadata")

	return m, nil
}

// RecordAuthFailure increments the auth_failures counter under reason.
// reason ∈ {expired, audience, signature, unknown_client, revoked,
// replayed_jti, malformed}.
func (m *Metrics) RecordAuthFailure(reason string) {
	if m == nil || m.AuthFailures == nil {
		return
	}
	m.AuthFailures.WithLabelValues(reason).Inc()
}

// RecordValidationFailure increments validation_failures under kind.
// kind ∈ {schema, semantic}.
func (m *Metrics) RecordValidationFailure(kind string) {
	if m == nil || m.ValidationFailures == nil {
		return
	}
	m.ValidationFailures.WithLabelValues(kind).Inc()
}

// RecordSubscriptionCreated, RecordSubscriptionUpdated, and
// RecordSubscriptionDeleted are nil-safe convenience wrappers used by
// handlers that may run without a metrics registry.

// RecordSubscriptionCreated increments the subscription_created counter.
func (m *Metrics) RecordSubscriptionCreated() {
	if m == nil || m.SubscriptionCreated == nil {
		return
	}
	m.SubscriptionCreated.Inc()
}

// RecordSubscriptionUpdated increments the subscription_updated counter.
func (m *Metrics) RecordSubscriptionUpdated() {
	if m == nil || m.SubscriptionUpdated == nil {
		return
	}
	m.SubscriptionUpdated.Inc()
}

// RecordSubscriptionDeleted increments the subscription_deleted counter.
func (m *Metrics) RecordSubscriptionDeleted() {
	if m == nil || m.SubscriptionDeleted == nil {
		return
	}
	m.SubscriptionDeleted.Inc()
}

// RecordTokenIssued increments the token_issued counter.
func (m *Metrics) RecordTokenIssued() {
	if m == nil || m.TokenIssued == nil {
		return
	}
	m.TokenIssued.Inc()
}

// RecordWSBindingTokenIssued increments the ws_binding_token_issued counter.
func (m *Metrics) RecordWSBindingTokenIssued() {
	if m == nil || m.WSBindingTokenIssued == nil {
		return
	}
	m.WSBindingTokenIssued.Inc()
}

// RecordActivatePanic increments the activate_panic counter. Called by
// the API handler's deferred recover() inside the activation goroutine
// (B-10).
func (m *Metrics) RecordActivatePanic() {
	if m == nil || m.ActivatePanicTotal == nil {
		return
	}
	m.ActivatePanicTotal.Inc()
}

// RecordRandFailure increments rand_failures_total. Called by handlers
// when crypto/rand.Read fails on a token-mint path (N-1).
func (m *Metrics) RecordRandFailure() {
	if m == nil || m.RandFailuresTotal == nil {
		return
	}
	m.RandFailuresTotal.Inc()
}

// RequestMiddleware returns a chi-friendly HTTP middleware that records
// requests_total and request_duration_seconds for every served request.
// The route label is the chi route pattern resolved by the router; if
// no route matched, the request URL path is used so unmatched requests
// still appear in metrics (low cardinality is preserved by the typical
// 404 NotFound handler being a constant).
func (m *Metrics) RequestMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if m == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			route := routePattern(r)
			method := r.Method
			status := strconv.Itoa(rec.status)
			m.RequestsTotal.WithLabelValues(method, route, status).Inc()
			m.RequestDuration.WithLabelValues(method, route).Observe(time.Since(start).Seconds())
		})
	}
}

// unmatchedRouteLabel is the constant `route` label used when the chi
// router did not resolve a registered pattern. Returning this constant
// instead of `r.URL.Path` keeps Prometheus cardinality bounded — a
// scanner pinging hundreds of distinct URLs no longer multiplies the
// fhir_subs_api_requests_total series (S-2.19).
const unmatchedRouteLabel = "<unmatched>"

func routePattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		if pat := rctx.RoutePattern(); pat != "" {
			return pat
		}
	}
	return unmatchedRouteLabel
}

// AuthGuard returns a chi-friendly middleware that gates a handler
// behind a fixed bearer token. Intended for the /metrics endpoint
// (S-2.18); production deployments pass the bearer secret via a
// kube-secret. An empty bearer disables the guard so dev / e2e setups
// keep working without configuration.
func AuthGuard(bearer string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if bearer == "" {
			return next
		}
		expected := "Bearer " + bearer
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != expected {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// statusRecorder captures the HTTP status the handler wrote so the
// middleware can label the requests counter without re-implementing
// chi's response wrapper.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteStatus bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteStatus {
		s.status = code
		s.wroteStatus = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteStatus {
		// Implicit 200 — note that we already initialized to 200, so
		// nothing to do other than mark the state.
		s.wroteStatus = true
	}
	return s.ResponseWriter.Write(b)
}
