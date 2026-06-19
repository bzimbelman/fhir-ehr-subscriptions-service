// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

// E2E test for OP #190 — bound parseRetryAfter (max + min floor).
//
// Threat model: a hostile or compromised subscriber returns
// Retry-After: 999999999 on a 503. Without the channel-level cap,
// the deliveries row would be pinned ~31 years into the future,
// which (a) exhausts the row's retry budget for an attacker-chosen
// window, (b) pollutes observability dashboards with bogus
// next_attempt_at timestamps, and (c) breaks any downstream caller
// that trusts RetryAfter at face value.
//
// This e2e drives the production resthook channel end-to-end against
// a real httptest TLS server returning the hostile header. No mocks.
// No stubs. Asserts:
//
//  1. The channel still classifies the response as Transient (5xx is
//     retry-eligible regardless of header values).
//  2. RetryAfter is clamped at the configured ceiling.
//  3. WARN log is emitted for the clamp (subscriber abuse signal).

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

// TestE2E_RestHook_RetryAfterDoSClamped — hostile subscriber returns
// Retry-After: 999999999 on 503. Channel must clamp the surfaced hint
// to MaxRetryAfter so the scheduler does not pin the deliveries row
// for years.
func TestE2E_RestHook_RetryAfterDoSClamped(t *testing.T) {
	t.Parallel()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "999999999") // ≈31 years
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	// Use a tighter MaxRetryAfter than the package default so the test
	// runs in <1s real-time but still exercises the same clamp path.
	const cap = 30 * time.Minute

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	ch, err := resthook.New(resthook.Options{
		HTTPClient:    srv.Client(),
		Logger:        logger,
		MaxRetryAfter: cap,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	env := channel.NotificationEnvelope{
		SubscriptionID:       uuid.New(),
		Sequence:             1,
		BundleBytes:          []byte(`{"resourceType":"Bundle","type":"subscription-notification"}`),
		BundleKind:           channel.BundleEventNotification,
		PayloadType:          channel.PayloadIDOnly,
		ContentType:          channel.ContentTypeFHIRJSON,
		Attempt:              1,
		CorrelationID:        uuid.New().String(),
		SubscriptionEndpoint: srv.URL + "/webhook",
		Deadline:             time.Now().Add(10 * time.Second),
	}

	out, err := ch.Deliver(context.Background(), env)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}

	if out.Kind != channel.OutcomeTransient {
		t.Fatalf("kind=%v reason=%q; want Transient (5xx remains retry-eligible)",
			out.Kind, out.Reason)
	}
	if out.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("StatusCode=%d; want 503", out.StatusCode)
	}
	if out.RetryAfter != cap {
		t.Fatalf("RetryAfter=%v; want %v (clamped to MaxRetryAfter)",
			out.RetryAfter, cap)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, `"level":"WARN"`) {
		t.Fatalf("expected WARN log on clamp; got: %s", logs)
	}
	if !strings.Contains(logs, "retry_after") {
		t.Errorf("expected log to name retry_after; got: %s", logs)
	}
}

// TestE2E_RestHook_RetryAfterFarFutureDateClamped — the same DoS with
// the alternate Retry-After form (HTTP-date 100 years out). Both forms
// must be bounded; an attacker who notices delta-seconds is clamped
// must not be able to pivot to HTTP-date.
func TestE2E_RestHook_RetryAfterFarFutureDateClamped(t *testing.T) {
	t.Parallel()
	farFuture := time.Now().Add(100 * 365 * 24 * time.Hour).UTC()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", farFuture.Format(http.TimeFormat))
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	ch, err := resthook.New(resthook.Options{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	env := channel.NotificationEnvelope{
		SubscriptionID:       uuid.New(),
		Sequence:             1,
		BundleBytes:          []byte(`{"resourceType":"Bundle"}`),
		BundleKind:           channel.BundleEventNotification,
		PayloadType:          channel.PayloadIDOnly,
		ContentType:          channel.ContentTypeFHIRJSON,
		Attempt:              1,
		CorrelationID:        uuid.New().String(),
		SubscriptionEndpoint: srv.URL + "/webhook",
		Deadline:             time.Now().Add(10 * time.Second),
	}

	out, err := ch.Deliver(context.Background(), env)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if out.Kind != channel.OutcomeTransient {
		t.Fatalf("kind=%v; want Transient", out.Kind)
	}
	// Default cap is 24h; allow ≤cap.
	if out.RetryAfter > resthook.DefaultMaxRetryAfter {
		t.Fatalf("RetryAfter=%v; want <= %v (clamped to default)",
			out.RetryAfter, resthook.DefaultMaxRetryAfter)
	}
	if out.RetryAfter != resthook.DefaultMaxRetryAfter {
		t.Errorf("RetryAfter=%v; want exactly %v",
			out.RetryAfter, resthook.DefaultMaxRetryAfter)
	}
}
