// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package resthook_test

// Tests for OP #190 — bound parseRetryAfter (max + min floor).
//
// Threat: a malicious or misconfigured subscriber returning a huge
// Retry-After (delta-seconds or HTTP-date) pins the deliveries row at
// far-future for years, denying retries. The scheduler eventually
// clamps to cfg.Retry.Max when computing the next attempt, but the
// channel layer is the load-bearing boundary because:
//
//  1. The DeliveryOutcome.RetryAfter is what's persisted/logged;
//     misleading values pollute observability and any downstream
//     scheduler that trusts the hint at face value.
//  2. Defense in depth: a future code path that uses RetryAfter without
//     re-clamping must not be exploitable from the network.
//
// All tests drive the real channel against a real httptest TLS server.
// No mocks. No stubs.

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/resthook"
)

// envelopeFor builds a minimal envelope pointing at the given URL.
func envelopeFor(endpoint string) channel.NotificationEnvelope {
	return channel.NotificationEnvelope{
		SubscriptionID:       uuid.New(),
		Sequence:             1,
		BundleBytes:          []byte(`{"resourceType":"Bundle","type":"subscription-notification"}`),
		BundleKind:           channel.BundleEventNotification,
		PayloadType:          channel.PayloadIDOnly,
		ContentType:          channel.ContentTypeFHIRJSON,
		Attempt:              1,
		CorrelationID:        uuid.New().String(),
		SubscriptionEndpoint: endpoint,
		Deadline:             time.Now().Add(10 * time.Second),
	}
}

// TestRetryAfterClampedAtMaxDeltaSeconds — the malicious case from OP #190:
// subscriber returns 503 + Retry-After: 999999999 (≈31 years). The channel
// must clamp to its configured MaxRetryAfter (default 24h) so a single
// hostile subscriber cannot pin a delivery row indefinitely.
func TestRetryAfterClampedAtMaxDeltaSeconds(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "999999999")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ch, err := resthook.New(resthook.Options{
		HTTPClient: srv.Client(),
		Metrics:    newFakeMetrics(),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	out, err := ch.Deliver(context.Background(), envelopeFor(srv.URL))
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if out.Kind != channel.OutcomeTransient {
		t.Fatalf("kind=%v reason=%q; want Transient", out.Kind, out.Reason)
	}
	if out.RetryAfter != resthook.DefaultMaxRetryAfter {
		t.Fatalf("RetryAfter=%v; want %v (DefaultMaxRetryAfter, clamped)",
			out.RetryAfter, resthook.DefaultMaxRetryAfter)
	}
}

// TestRetryAfterClampedAtMaxHTTPDate — alternate Retry-After form. A
// far-future HTTP-date (50 years) must clamp identically to
// delta-seconds. RFC 7231 allows either form; both must be bounded.
func TestRetryAfterClampedAtMaxHTTPDate(t *testing.T) {
	t.Parallel()
	farFuture := time.Now().Add(50 * 365 * 24 * time.Hour).UTC()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", farFuture.Format(http.TimeFormat))
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ch, err := resthook.New(resthook.Options{
		HTTPClient: srv.Client(),
		Metrics:    newFakeMetrics(),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	out, err := ch.Deliver(context.Background(), envelopeFor(srv.URL))
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if out.RetryAfter != resthook.DefaultMaxRetryAfter {
		t.Fatalf("RetryAfter=%v; want %v (DefaultMaxRetryAfter, clamped)",
			out.RetryAfter, resthook.DefaultMaxRetryAfter)
	}
}

// TestRetryAfterCustomMaxClamp — operator sets a tighter cap (30m). A
// 999999999 header must clamp to 30m, not the package default. Proves
// the cap is wired through Options, not hardcoded.
func TestRetryAfterCustomMaxClamp(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "999999999")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	customMax := 30 * time.Minute
	ch, err := resthook.New(resthook.Options{
		HTTPClient:    srv.Client(),
		Metrics:       newFakeMetrics(),
		MaxRetryAfter: customMax,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	out, err := ch.Deliver(context.Background(), envelopeFor(srv.URL))
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if out.RetryAfter != customMax {
		t.Fatalf("RetryAfter=%v; want %v (custom cap)", out.RetryAfter, customMax)
	}
}

// TestRetryAfterMinFloorClamp — subscriber sends Retry-After: 1, but
// operator's MinRetryAfter is 5s. Channel clamps up to 5s so the
// scheduler doesn't burn through its retry budget on a tight loop the
// subscriber requested.
func TestRetryAfterMinFloorClamp(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	customMin := 5 * time.Second
	ch, err := resthook.New(resthook.Options{
		HTTPClient:    srv.Client(),
		Metrics:       newFakeMetrics(),
		MinRetryAfter: customMin,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	out, err := ch.Deliver(context.Background(), envelopeFor(srv.URL))
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if out.RetryAfter != customMin {
		t.Fatalf("RetryAfter=%v; want %v (clamped to MinRetryAfter)",
			out.RetryAfter, customMin)
	}
}

// TestRetryAfterZeroDistinctFromMissing — the second OP #190 bug.
// Today, parseRetryAfter returns 0 for both "missing" and "value=0";
// the scheduler can't tell "subscriber said retry now" from "subscriber
// gave no hint." After the fix:
//   - Retry-After: 0   → 1ns (a positive sentinel: "retry now, but
//     wins the >0 check downstream so the scheduler honors the floor")
//   - missing header   → 0  ("no hint, scheduler uses default backoff")
func TestRetryAfterZeroDistinctFromMissing(t *testing.T) {
	t.Parallel()

	t.Run("zero-header", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()

		ch, err := resthook.New(resthook.Options{
			HTTPClient: srv.Client(),
			Metrics:    newFakeMetrics(),
		})
		if err != nil {
			t.Fatalf("new: %v", err)
		}
		out, err := ch.Deliver(context.Background(), envelopeFor(srv.URL))
		if err != nil {
			t.Fatalf("deliver: %v", err)
		}
		if out.RetryAfter <= 0 {
			t.Fatalf("RetryAfter=%v; want >0 for explicit Retry-After: 0 (must distinguish from missing)",
				out.RetryAfter)
		}
		// Should be small — 1ns is the documented sentinel.
		if out.RetryAfter > time.Second {
			t.Errorf("RetryAfter=%v; want a small positive sentinel for Retry-After: 0",
				out.RetryAfter)
		}
	})

	t.Run("missing-header", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// Deliberately no Retry-After header.
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()

		ch, err := resthook.New(resthook.Options{
			HTTPClient: srv.Client(),
			Metrics:    newFakeMetrics(),
		})
		if err != nil {
			t.Fatalf("new: %v", err)
		}
		out, err := ch.Deliver(context.Background(), envelopeFor(srv.URL))
		if err != nil {
			t.Fatalf("deliver: %v", err)
		}
		if out.RetryAfter != 0 {
			t.Fatalf("RetryAfter=%v; want 0 for missing header", out.RetryAfter)
		}
	})
}

// TestRetryAfterNegativePastDate — RFC 7231 HTTP-date in the past is
// effectively "retry now". This must produce the same positive
// sentinel as Retry-After: 0, not 0 (which would be indistinguishable
// from "missing").
func TestRetryAfterNegativePastDate(t *testing.T) {
	t.Parallel()
	pastDate := time.Now().Add(-1 * time.Hour).UTC()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", pastDate.Format(http.TimeFormat))
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ch, err := resthook.New(resthook.Options{
		HTTPClient: srv.Client(),
		Metrics:    newFakeMetrics(),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	out, err := ch.Deliver(context.Background(), envelopeFor(srv.URL))
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if out.RetryAfter <= 0 {
		t.Fatalf("RetryAfter=%v; want >0 for past HTTP-date (retry-now sentinel)",
			out.RetryAfter)
	}
	if out.RetryAfter > time.Second {
		t.Errorf("RetryAfter=%v; want a small positive sentinel for past date",
			out.RetryAfter)
	}
}

// TestRetryAfterClampLogsWarn — when clamping to the cap, the channel
// emits a WARN-level log entry naming the clamp. Operators rely on
// this signal to spot misbehaving subscribers; silent clamping is a
// gap in the observability story (audit acceptance criteria explicit).
func TestRetryAfterClampLogsWarn(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "999999999")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ch, err := resthook.New(resthook.Options{
		HTTPClient: srv.Client(),
		Metrics:    newFakeMetrics(),
		Logger:     logger,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, err := ch.Deliver(context.Background(), envelopeFor(srv.URL)); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, `"level":"WARN"`) {
		t.Fatalf("expected WARN log on clamp; got: %s", got)
	}
	if !strings.Contains(got, "retry_after") && !strings.Contains(got, "retry-after") {
		t.Errorf("expected log to name retry_after; got: %s", got)
	}
}

// TestRetryAfterValidValuePassesThrough — sanity: a sane Retry-After
// value (60s) must NOT be clamped. We're bounding hostile inputs, not
// re-implementing the scheduler.
func TestRetryAfterValidValuePassesThrough(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ch, err := resthook.New(resthook.Options{
		HTTPClient: srv.Client(),
		Metrics:    newFakeMetrics(),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	out, err := ch.Deliver(context.Background(), envelopeFor(srv.URL))
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if out.RetryAfter != 60*time.Second {
		t.Fatalf("RetryAfter=%v; want 60s (unclamped, sane value)", out.RetryAfter)
	}
}
