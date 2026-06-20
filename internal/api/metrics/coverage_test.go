// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package metrics_test

// Coverage tests for OP #309. Targets: every Record* method (including
// the nil-receiver and nil-counter early-return branches), AuthGuard
// (empty-bearer pass-through, valid bearer, missing/wrong bearer),
// routePattern's unmatched branch, and New's already-registered branch.
// No mocks: real prometheus.NewRegistry, real httptest.NewRecorder.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	apimetrics "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/metrics"
)

func newMetrics(t *testing.T) *apimetrics.Metrics {
	t.Helper()
	m, err := apimetrics.New(prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

func TestRecordSubscriptionCreated_IncrementsCounter(t *testing.T) {
	t.Parallel()
	m := newMetrics(t)
	m.RecordSubscriptionCreated()
	m.RecordSubscriptionCreated()
	if got := testutil.ToFloat64(m.SubscriptionCreated); got != 2 {
		t.Errorf("SubscriptionCreated = %v, want 2", got)
	}
}

func TestRecordSubscriptionUpdated_IncrementsCounter(t *testing.T) {
	t.Parallel()
	m := newMetrics(t)
	m.RecordSubscriptionUpdated()
	if got := testutil.ToFloat64(m.SubscriptionUpdated); got != 1 {
		t.Errorf("SubscriptionUpdated = %v, want 1", got)
	}
}

func TestRecordSubscriptionDeleted_IncrementsCounter(t *testing.T) {
	t.Parallel()
	m := newMetrics(t)
	m.RecordSubscriptionDeleted()
	if got := testutil.ToFloat64(m.SubscriptionDeleted); got != 1 {
		t.Errorf("SubscriptionDeleted = %v, want 1", got)
	}
}

func TestRecordTokenIssued_IncrementsCounter(t *testing.T) {
	t.Parallel()
	m := newMetrics(t)
	m.RecordTokenIssued()
	if got := testutil.ToFloat64(m.TokenIssued); got != 1 {
		t.Errorf("TokenIssued = %v, want 1", got)
	}
}

func TestRecordWSBindingTokenIssued_IncrementsCounter(t *testing.T) {
	t.Parallel()
	m := newMetrics(t)
	m.RecordWSBindingTokenIssued()
	if got := testutil.ToFloat64(m.WSBindingTokenIssued); got != 1 {
		t.Errorf("WSBindingTokenIssued = %v, want 1", got)
	}
}

func TestRecordJWKSSingleflightCollision_IncrementsCounter(t *testing.T) {
	t.Parallel()
	m := newMetrics(t)
	m.RecordJWKSSingleflightCollision()
	m.RecordJWKSSingleflightCollision()
	m.RecordJWKSSingleflightCollision()
	if got := testutil.ToFloat64(m.JWKSSingleflightCollisions); got != 3 {
		t.Errorf("JWKSSingleflightCollisions = %v, want 3", got)
	}
}

func TestRecordActivatePanic_IncrementsCounter(t *testing.T) {
	t.Parallel()
	m := newMetrics(t)
	m.RecordActivatePanic()
	if got := testutil.ToFloat64(m.ActivatePanicTotal); got != 1 {
		t.Errorf("ActivatePanicTotal = %v, want 1", got)
	}
}

func TestRecordRandFailure_IncrementsCounter(t *testing.T) {
	t.Parallel()
	m := newMetrics(t)
	m.RecordRandFailure()
	if got := testutil.ToFloat64(m.RandFailuresTotal); got != 1 {
		t.Errorf("RandFailuresTotal = %v, want 1", got)
	}
}

func TestRecordAuthFailure_IncrementsCounter(t *testing.T) {
	t.Parallel()
	m := newMetrics(t)
	m.RecordAuthFailure("expired")
	m.RecordAuthFailure("expired")
	if got := testutil.ToFloat64(m.AuthFailures.WithLabelValues("expired")); got != 2 {
		t.Errorf("AuthFailures[expired] = %v, want 2", got)
	}
}

func TestRecordValidationFailure_IncrementsCounter(t *testing.T) {
	t.Parallel()
	m := newMetrics(t)
	m.RecordValidationFailure("schema")
	if got := testutil.ToFloat64(m.ValidationFailures.WithLabelValues("schema")); got != 1 {
		t.Errorf("ValidationFailures[schema] = %v, want 1", got)
	}
}

func TestAllRecorders_NilReceiverIsNoOp(t *testing.T) {
	t.Parallel()
	var m *apimetrics.Metrics
	// All Record* methods accept nil receiver and must not panic.
	m.RecordAuthFailure("malformed")
	m.RecordValidationFailure("schema")
	m.RecordSubscriptionCreated()
	m.RecordSubscriptionUpdated()
	m.RecordSubscriptionDeleted()
	m.RecordTokenIssued()
	m.RecordWSBindingTokenIssued()
	m.RecordJWKSSingleflightCollision()
	m.RecordActivatePanic()
	m.RecordRandFailure()
}

func TestAllRecorders_NilCounterIsNoOp(t *testing.T) {
	t.Parallel()
	// A zero-value Metrics has all counters nil. Every Record* must
	// guard on the specific counter being nil without panicking.
	m := &apimetrics.Metrics{}
	m.RecordAuthFailure("malformed")
	m.RecordValidationFailure("schema")
	m.RecordSubscriptionCreated()
	m.RecordSubscriptionUpdated()
	m.RecordSubscriptionDeleted()
	m.RecordTokenIssued()
	m.RecordWSBindingTokenIssued()
	m.RecordJWKSSingleflightCollision()
	m.RecordActivatePanic()
	m.RecordRandFailure()
}

func TestAuthGuard_EmptyBearerPassthrough(t *testing.T) {
	t.Parallel()
	called := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	})
	mw := apimetrics.AuthGuard("")(next)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	mw.ServeHTTP(rec, req)

	if !called {
		t.Error("expected next handler to run on empty bearer (guard disabled)")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestAuthGuard_ValidBearerAllowed(t *testing.T) {
	t.Parallel()
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	mw := apimetrics.AuthGuard("secret-token")(next)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	mw.ServeHTTP(rec, req)

	if !called {
		t.Error("expected next handler to run with correct bearer")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestAuthGuard_MissingBearerRejected(t *testing.T) {
	t.Parallel()
	called := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	})
	mw := apimetrics.AuthGuard("secret-token")(next)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	mw.ServeHTTP(rec, req)

	if called {
		t.Error("expected next handler NOT to run with missing bearer")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestAuthGuard_WrongBearerRejected(t *testing.T) {
	t.Parallel()
	called := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	})
	mw := apimetrics.AuthGuard("secret-token")(next)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	mw.ServeHTTP(rec, req)

	if called {
		t.Error("expected next handler NOT to run with wrong bearer")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestRequestMiddleware_NilMetricsPassthrough(t *testing.T) {
	t.Parallel()
	var m *apimetrics.Metrics
	called := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	})
	mw := m.RequestMiddleware()(next)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metadata", nil)
	mw.ServeHTTP(rec, req)
	if !called {
		t.Error("expected next handler to run when metrics is nil")
	}
}

func TestRequestMiddleware_RecordsImplicit200(t *testing.T) {
	t.Parallel()
	m := newMetrics(t)

	// Handler that calls Write without calling WriteHeader: implicit 200.
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mw := m.RequestMiddleware()(next)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	mw.ServeHTTP(rec, req)

	// Route falls through to <unmatched> since chi.RouteContext is nil.
	got := testutil.ToFloat64(m.RequestsTotal.WithLabelValues("GET", "<unmatched>", "200"))
	if got != 1 {
		t.Errorf("RequestsTotal[GET,<unmatched>,200] = %v, want 1", got)
	}
}

func TestRequestMiddleware_RecordsExplicitStatus(t *testing.T) {
	t.Parallel()
	m := newMetrics(t)

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		// Subsequent Write calls should NOT change recorded status (idempotent first-write).
		_, _ = w.Write([]byte("err"))
	})
	mw := m.RequestMiddleware()(next)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/y", strings.NewReader("body"))
	mw.ServeHTTP(rec, req)

	got := testutil.ToFloat64(m.RequestsTotal.WithLabelValues("POST", "<unmatched>", "500"))
	if got != 1 {
		t.Errorf("RequestsTotal[POST,<unmatched>,500] = %v, want 1", got)
	}
}

func TestNew_AlreadyRegisteredIsNotFatal(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	if _, err := apimetrics.New(reg); err != nil {
		t.Fatalf("first New: %v", err)
	}
	// A second New on the same registry hits the AlreadyRegisteredError
	// branch for every collector; per the implementation, that is
	// silently swallowed (the existing registration is reused).
	if _, err := apimetrics.New(reg); err != nil {
		t.Fatalf("second New: %v", err)
	}
}

func TestNew_NilRegistererUsesFreshRegistry(t *testing.T) {
	t.Parallel()
	m, err := apimetrics.New(nil)
	if err != nil {
		t.Fatalf("New(nil): %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil metrics for nil registerer")
	}
}
