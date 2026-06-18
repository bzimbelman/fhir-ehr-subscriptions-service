// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package metrics_test

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/metrics"
)

// LLD §4: every metric must use the fhir_subs_ prefix (ADR 0008 #10).
// The MetricsEmitter rejects metric names lacking the prefix at registration.
func TestNewCounter_RejectsMissingPrefix(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	em := metrics.New(reg)

	_, err := em.NewCounter(metrics.CounterOpts{
		Name: "deliveries_total",
		Help: "missing prefix",
	})
	if err == nil {
		t.Fatalf("expected error for missing fhir_subs_ prefix")
	}
	if !errors.Is(err, metrics.ErrInvalidName) {
		t.Fatalf("expected ErrInvalidName; got %v", err)
	}
}

func TestNewCounter_AcceptsCorrectPrefix(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	em := metrics.New(reg)

	c, err := em.NewCounter(metrics.CounterOpts{
		Name:   "fhir_subs_test_total",
		Help:   "test counter",
		Labels: []string{"outcome"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c.Inc(prometheus.Labels{"outcome": "ok"})
}

func TestNewHistogram_AcceptsPrefix(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	em := metrics.New(reg)
	h, err := em.NewHistogram(metrics.HistogramOpts{
		Name:    "fhir_subs_stage_duration_seconds",
		Help:    "stage duration",
		Labels:  []string{"stage"},
		Buckets: []float64{0.001, 0.01, 0.1, 1.0},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	h.Observe(0.5, prometheus.Labels{"stage": "translate"})
}

func TestNewGauge_AcceptsPrefix(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	em := metrics.New(reg)
	g, err := em.NewGauge(metrics.GaugeOpts{
		Name:   "fhir_subs_subscription_status",
		Help:   "current state",
		Labels: []string{"subscription_id", "status"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	g.Set(1, prometheus.Labels{"subscription_id": "sub-1", "status": "active"})
}

// LLD §4.2: subscription_id is forbidden on histograms and counters.
// It is permitted only on gauges (subscription_status, heartbeat_lag_seconds).
func TestNewCounter_RejectsSubscriptionIDLabel(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	em := metrics.New(reg)

	_, err := em.NewCounter(metrics.CounterOpts{
		Name:   "fhir_subs_deliveries_total",
		Help:   "with forbidden label",
		Labels: []string{"subscription_id"},
	})
	if err == nil {
		t.Fatalf("expected error for subscription_id on counter")
	}
	if !errors.Is(err, metrics.ErrInvalidLabel) {
		t.Fatalf("expected ErrInvalidLabel; got %v", err)
	}
}

func TestNewHistogram_RejectsSubscriptionIDLabel(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	em := metrics.New(reg)

	_, err := em.NewHistogram(metrics.HistogramOpts{
		Name:   "fhir_subs_per_subscription_latency_seconds",
		Help:   "should not be allowed",
		Labels: []string{"subscription_id"},
	})
	if err == nil {
		t.Fatalf("expected error for subscription_id on histogram")
	}
	if !errors.Is(err, metrics.ErrInvalidLabel) {
		t.Fatalf("expected ErrInvalidLabel; got %v", err)
	}
}

// LLD §4.2: peer_addr is allowed only on listener counters; never on histograms.
func TestNewHistogram_RejectsPeerAddrLabel(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	em := metrics.New(reg)

	_, err := em.NewHistogram(metrics.HistogramOpts{
		Name:   "fhir_subs_hl7_message_bytes",
		Help:   "histogram with peer_addr",
		Labels: []string{"peer_addr"},
	})
	if err == nil {
		t.Fatalf("expected error for peer_addr on histogram")
	}
	if !errors.Is(err, metrics.ErrInvalidLabel) {
		t.Fatalf("expected ErrInvalidLabel; got %v", err)
	}
}

// peer_addr is allowed only on names ending in "_received_total" (listener counter).
func TestNewCounter_RejectsPeerAddrOnNonListenerCounter(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	em := metrics.New(reg)

	_, err := em.NewCounter(metrics.CounterOpts{
		Name:   "fhir_subs_deliveries_total",
		Help:   "deliveries with peer_addr is forbidden",
		Labels: []string{"peer_addr"},
	})
	if err == nil {
		t.Fatalf("expected error for peer_addr on non-listener counter")
	}
	if !errors.Is(err, metrics.ErrInvalidLabel) {
		t.Fatalf("expected ErrInvalidLabel; got %v", err)
	}
}

func TestNewCounter_AcceptsPeerAddrOnListenerCounter(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	em := metrics.New(reg)

	_, err := em.NewCounter(metrics.CounterOpts{
		Name:   "fhir_subs_hl7_messages_received_total",
		Help:   "listener counter with peer_addr is allowed",
		Labels: []string{"listener_endpoint", "peer_addr"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Re-registration with the same name and same shape must succeed (idempotent for
// startup metric registration).
func TestNewCounter_DuplicateRegistrationReturnsExisting(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	em := metrics.New(reg)

	_, err := em.NewCounter(metrics.CounterOpts{
		Name:   "fhir_subs_audit_writes_total",
		Help:   "audit writes",
		Labels: []string{"outcome"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = em.NewCounter(metrics.CounterOpts{
		Name:   "fhir_subs_audit_writes_total",
		Help:   "audit writes",
		Labels: []string{"outcome"},
	})
	if err != nil {
		t.Fatalf("re-registration must succeed; got: %v", err)
	}
}

func TestPrometheusHandler_ServesMetrics(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	em := metrics.New(reg)
	c, err := em.NewCounter(metrics.CounterOpts{
		Name: "fhir_subs_handler_test_total",
		Help: "handler test counter",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	c.Inc(nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	em.Handler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200; got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "fhir_subs_handler_test_total") {
		t.Fatalf("expected metric in body; got: %s", body)
	}
}

// The startup inventory must be registered: ensure registration helper returns the
// canonical metric set.
func TestRegisterStartupInventory(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	em := metrics.New(reg)

	inv, err := metrics.RegisterStartupInventory(em)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}

	if inv.AuditWritesTotal == nil {
		t.Fatalf("expected AuditWritesTotal counter")
	}
	if inv.AuditChainInvalidTotal == nil {
		t.Fatalf("expected AuditChainInvalidTotal counter")
	}
	if inv.AuditSinkFailuresTotal == nil {
		t.Fatalf("expected AuditSinkFailuresTotal counter")
	}
	if inv.LoggingPHIDroppedTotal == nil {
		t.Fatalf("expected LoggingPHIDroppedTotal counter")
	}
	if inv.MetricsRegistered == nil {
		t.Fatalf("expected MetricsRegistered gauge")
	}
}
