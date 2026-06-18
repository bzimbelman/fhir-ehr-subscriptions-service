// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package observability_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/metrics"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/topics/catalog"
)

// Story #61 wires four counter sets the audit flagged PARTIAL: matcher
// row-attempts, submatcher row-attempts, topics_rejected_total, and
// topic_overridden_total. The startup inventory exposes typed handles
// for them so the host can install adapters that forward call-site
// emits into the Prometheus collectors.
func TestStory61_StartupInventory_RegistersNewCounters(t *testing.T) {
	t.Parallel()

	mod, _, err := observability.Start(context.Background(), observability.Config{
		Logging: observability.LoggingConfig{Level: "info", Format: "json"},
	}, observability.Context{
		StoragePool: newFakeStore(),
		Clock:       func() time.Time { return time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = mod.Shutdown(context.Background()) }()

	inv := mod.Inventory()
	if inv.MatcherRowAttemptsTotal == nil {
		t.Fatalf("expected MatcherRowAttemptsTotal in inventory")
	}
	if inv.SubmatcherRowAttemptsTotal == nil {
		t.Fatalf("expected SubmatcherRowAttemptsTotal in inventory")
	}
	if inv.TopicsRejectedTotal == nil {
		t.Fatalf("expected TopicsRejectedTotal in inventory")
	}
	if inv.TopicOverriddenTotal == nil {
		t.Fatalf("expected TopicOverriddenTotal in inventory")
	}

	// /metrics endpoint exposes all four (pre-seeded for the closed-domain
	// label sets so dashboards see them at scrape time).
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	mod.PrometheusHandler().ServeHTTP(rec, req)
	body := rec.Body.String()

	for _, want := range []string{
		"fhir_subs_matcher_row_attempts_total",
		"fhir_subs_submatcher_row_attempts_total",
		"fhir_subs_topics_rejected_total",
		"fhir_subs_topic_overridden_total",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics missing %q", want)
		}
	}
}

// The matcher row-attempts counter must be pre-seeded with all three
// outcome labels so dashboards see a stable series at scrape time before
// the first claim cycle lands.
func TestStory61_MatcherRowAttempts_OutcomeLabelsPreSeeded(t *testing.T) {
	t.Parallel()
	mod, _, err := observability.Start(context.Background(), observability.Config{
		Logging: observability.LoggingConfig{Level: "info", Format: "json"},
	}, observability.Context{
		StoragePool: newFakeStore(),
		Clock:       func() time.Time { return time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = mod.Shutdown(context.Background()) }()

	rec := httptest.NewRecorder()
	mod.PrometheusHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()

	for _, outcome := range []string{"processed", "deferred", "error"} {
		want := `fhir_subs_matcher_row_attempts_total{outcome="` + outcome + `"} 0`
		if !strings.Contains(body, want) {
			t.Errorf("/metrics missing pre-seeded %s", want)
		}
		want = `fhir_subs_submatcher_row_attempts_total{outcome="` + outcome + `"} 0`
		if !strings.Contains(body, want) {
			t.Errorf("/metrics missing pre-seeded %s", want)
		}
	}
}

// MatcherRowAttemptsTotal must be a Counter with a single "outcome"
// label. NewCounter is idempotent on (name, labels); registering a
// second time with the canonical shape returns the existing collector.
// Registering with a different label shape returns ErrInvalidName.
func TestStory61_MatcherRowAttempts_LabelShape(t *testing.T) {
	t.Parallel()
	mod, h, err := observability.Start(context.Background(), observability.Config{
		Logging: observability.LoggingConfig{Level: "info", Format: "json"},
	}, observability.Context{
		StoragePool: newFakeStore(),
		Clock:       func() time.Time { return time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = mod.Shutdown(context.Background()) }()

	// Same shape — should return the same handle without error.
	if _, err := h.Metrics.NewCounter(metrics.CounterOpts{
		Name:   "fhir_subs_matcher_row_attempts_total",
		Help:   "ignored on re-register",
		Labels: []string{"outcome"},
	}); err != nil {
		t.Fatalf("idempotent re-register: %v", err)
	}

	// Different shape — must error.
	if _, err := h.Metrics.NewCounter(metrics.CounterOpts{
		Name:   "fhir_subs_matcher_row_attempts_total",
		Help:   "wrong shape",
		Labels: []string{"different"},
	}); err == nil {
		t.Fatalf("expected error registering with different labels")
	}
}

// MatcherRowAttemptsTotal must be Inc-able via the inventory handle the
// host adapter installs into matcher.SetMetricsEmitter. The adapter
// itself is private; we exercise the inventory directly to assert that
// the typed counter path produces the expected scrape output.
func TestStory61_MatcherRowAttempts_IncrementsThroughInventory(t *testing.T) {
	t.Parallel()
	mod, _, err := observability.Start(context.Background(), observability.Config{
		Logging: observability.LoggingConfig{Level: "info", Format: "json"},
	}, observability.Context{
		StoragePool: newFakeStore(),
		Clock:       func() time.Time { return time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = mod.Shutdown(context.Background()) }()

	inv := mod.Inventory()
	inv.MatcherRowAttemptsTotal.Inc(prometheus.Labels{"outcome": "processed"})
	inv.MatcherRowAttemptsTotal.Inc(prometheus.Labels{"outcome": "error"})
	inv.MatcherRowAttemptsTotal.Inc(prometheus.Labels{"outcome": "error"})

	rec := httptest.NewRecorder()
	mod.PrometheusHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()

	if !strings.Contains(body, `fhir_subs_matcher_row_attempts_total{outcome="processed"} 1`) {
		t.Errorf("expected processed=1 in body; got: %s", body)
	}
	if !strings.Contains(body, `fhir_subs_matcher_row_attempts_total{outcome="error"} 2`) {
		t.Errorf("expected error=2 in body; got: %s", body)
	}
}

// PublishTopicCatalogReport iterates Catalog.Rejected() and
// Catalog.Overridden() and bumps the corresponding counter once per
// entry, with the recorded origin / reason / source labels.
func TestStory61_PublishTopicCatalogReport_BumpsCounters(t *testing.T) {
	t.Parallel()
	mod, _, err := observability.Start(context.Background(), observability.Config{
		Logging: observability.LoggingConfig{Level: "info", Format: "json"},
	}, observability.Context{
		StoragePool: newFakeStore(),
		Clock:       func() time.Time { return time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = mod.Shutdown(context.Background()) }()

	mod.PublishTopicCatalogReport(catalog.Report{
		Rejected: []catalog.Rejection{
			{Origin: "operator", URL: "http://test/topic-a", Reason: "compile_failure"},
			{Origin: "operator", URL: "http://test/topic-b", Reason: "compile_failure"},
		},
		Overridden: []catalog.Override{
			{
				URL:        "http://test/topic-c",
				FromOrigin: "/etc/topics/operator/topic-c.json",
				FromSource: catalog.SourceOperator,
				ToOrigin:   "builtin://default/topic-c",
				ToSource:   catalog.SourceBuiltIn,
				Reason:     "operator copy compile error",
			},
		},
	})

	rec := httptest.NewRecorder()
	mod.PrometheusHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()

	if !strings.Contains(body, `fhir_subs_topics_rejected_total{origin="operator",reason="compile_failure"} 2`) {
		t.Errorf("expected operator/compile_failure=2 in body; got: %s", body)
	}
	if !strings.Contains(body, `fhir_subs_topic_overridden_total{from="operator",to="builtin"} 1`) {
		t.Errorf("expected from=operator,to=builtin=1 in body; got: %s", body)
	}
}

// Topics_rejected_total carries (origin, reason); topic_overridden_total
// carries (from, to). Both must be a Counter and both must accept the
// canonical labels at registration time.
func TestStory61_TopicCatalogCounters_LabelShape(t *testing.T) {
	t.Parallel()
	mod, h, err := observability.Start(context.Background(), observability.Config{
		Logging: observability.LoggingConfig{Level: "info", Format: "json"},
	}, observability.Context{
		StoragePool: newFakeStore(),
		Clock:       func() time.Time { return time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = mod.Shutdown(context.Background()) }()

	// Idempotent re-register with the canonical shape.
	if _, err := h.Metrics.NewCounter(metrics.CounterOpts{
		Name:   "fhir_subs_topics_rejected_total",
		Help:   "ignored on re-register",
		Labels: []string{"origin", "reason"},
	}); err != nil {
		t.Fatalf("idempotent re-register topics_rejected: %v", err)
	}
	if _, err := h.Metrics.NewCounter(metrics.CounterOpts{
		Name:   "fhir_subs_topic_overridden_total",
		Help:   "ignored on re-register",
		Labels: []string{"from", "to"},
	}); err != nil {
		t.Fatalf("idempotent re-register topic_overridden: %v", err)
	}
}
