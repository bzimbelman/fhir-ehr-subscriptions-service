// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	apimetrics "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/metrics"
)

func TestNew_RegistersAllMetrics(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m, err := apimetrics.New(reg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil metrics")
	}

	// Verify each required metric exists by gathering and checking name.
	expected := []string{
		"fhir_subs_api_requests_total",
		"fhir_subs_api_request_duration_seconds",
		"fhir_subs_api_auth_failures_total",
		"fhir_subs_api_subscription_created_total",
		"fhir_subs_api_subscription_updated_total",
		"fhir_subs_api_subscription_deleted_total",
		"fhir_subs_api_validation_failures_total",
		"fhir_subs_api_token_issued_total",
		"fhir_subs_api_ws_binding_token_issued_total",
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	have := map[string]bool{}
	for _, mf := range mfs {
		have[mf.GetName()] = true
	}
	for _, name := range expected {
		if !have[name] {
			t.Errorf("missing metric %q", name)
		}
	}
}

func TestSubscriptionCounters_Increment(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m, err := apimetrics.New(reg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	m.SubscriptionCreated.Inc()
	m.SubscriptionUpdated.Inc()
	m.SubscriptionUpdated.Inc()
	m.SubscriptionDeleted.Inc()

	if got := testutil.ToFloat64(m.SubscriptionCreated); got != 1 {
		t.Errorf("created = %v; want 1", got)
	}
	if got := testutil.ToFloat64(m.SubscriptionUpdated); got != 2 {
		t.Errorf("updated = %v; want 2", got)
	}
	if got := testutil.ToFloat64(m.SubscriptionDeleted); got != 1 {
		t.Errorf("deleted = %v; want 1", got)
	}
}

func TestAuthFailureCounter_LabeledByReason(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m, err := apimetrics.New(reg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, reason := range []string{"expired", "audience", "signature", "unknown_client", "revoked", "replayed_jti", "malformed"} {
		m.RecordAuthFailure(reason)
	}

	for _, reason := range []string{"expired", "audience", "signature", "unknown_client", "revoked", "replayed_jti", "malformed"} {
		got := testutil.ToFloat64(m.AuthFailures.WithLabelValues(reason))
		if got != 1 {
			t.Errorf("auth_failures{reason=%q} = %v; want 1", reason, got)
		}
	}
}

func TestValidationFailureCounter_LabeledByKind(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m, err := apimetrics.New(reg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	m.RecordValidationFailure("schema")
	m.RecordValidationFailure("schema")
	m.RecordValidationFailure("semantic")

	if got := testutil.ToFloat64(m.ValidationFailures.WithLabelValues("schema")); got != 2 {
		t.Errorf("validation_failures{kind=schema} = %v; want 2", got)
	}
	if got := testutil.ToFloat64(m.ValidationFailures.WithLabelValues("semantic")); got != 1 {
		t.Errorf("validation_failures{kind=semantic} = %v; want 1", got)
	}
}

func TestTokenAndWsCounters_Increment(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m, err := apimetrics.New(reg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.TokenIssued.Inc()
	m.WSBindingTokenIssued.Inc()
	m.WSBindingTokenIssued.Inc()

	if got := testutil.ToFloat64(m.TokenIssued); got != 1 {
		t.Errorf("token_issued = %v; want 1", got)
	}
	if got := testutil.ToFloat64(m.WSBindingTokenIssued); got != 2 {
		t.Errorf("ws_binding_token_issued = %v; want 2", got)
	}
}

func TestRequestMiddleware_RecordsRequestsTotal(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m, err := apimetrics.New(reg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	r := chi.NewRouter()
	r.Use(m.RequestMiddleware())
	r.Get("/Subscription/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/Subscription/abc", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
	}

	got := testutil.ToFloat64(m.RequestsTotal.WithLabelValues("GET", "/Subscription/{id}", "200"))
	if got != 3 {
		t.Errorf("requests_total = %v; want 3", got)
	}
}

func TestRequestMiddleware_RecordsLatencyHistogram(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m, err := apimetrics.New(reg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	r := chi.NewRouter()
	r.Use(m.RequestMiddleware())
	r.Get("/metadata", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/metadata", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	count := testutil.CollectAndCount(m.RequestDuration, "fhir_subs_api_request_duration_seconds")
	if count != 1 {
		t.Errorf("histogram series count = %d; want 1", count)
	}
}

func TestRequestMiddleware_RecordsStatusFromError(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m, err := apimetrics.New(reg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	r := chi.NewRouter()
	r.Use(m.RequestMiddleware())
	r.Get("/Subscription/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	req := httptest.NewRequest(http.MethodGet, "/Subscription/x", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	got := testutil.ToFloat64(m.RequestsTotal.WithLabelValues("GET", "/Subscription/{id}", "404"))
	if got != 1 {
		t.Errorf("404 series = %v; want 1", got)
	}
}

func TestRequestMiddleware_DefaultStatusIs200(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m, err := apimetrics.New(reg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	r := chi.NewRouter()
	r.Use(m.RequestMiddleware())
	// Handler doesn't call WriteHeader explicitly; default should be 200.
	r.Get("/Subscription", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})

	req := httptest.NewRequest(http.MethodGet, "/Subscription", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	got := testutil.ToFloat64(m.RequestsTotal.WithLabelValues("GET", "/Subscription", "200"))
	if got != 1 {
		t.Errorf("implicit 200 = %v; want 1", got)
	}
}
