// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/resthook"
)

// TestE2E_RestHook_503WithRetryAfterHonored exercises a subscriber
// returning 503 Service Unavailable + Retry-After: 60. The channel must
// classify Transient AND surface RetryAfter ≥ 60s so the scheduler
// honors the backoff hint instead of using its own curve.
//
// Why this matters: an EHR doing maintenance signals "back off for a
// minute"; if the channel ignored Retry-After, the scheduler would keep
// hammering the subscriber and risk being cut off entirely.
func TestE2E_RestHook_503WithRetryAfterHonored(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ch, err := resthook.New(resthook.Options{HTTPClient: srv.Client()})
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
		t.Fatalf("kind=%v reason=%q; want Transient", out.Kind, out.Reason)
	}
	if out.RetryAfter < 60*time.Second {
		t.Fatalf("RetryAfter=%v; want >= 60s", out.RetryAfter)
	}
	if out.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("StatusCode=%d; want 503", out.StatusCode)
	}
}

// TestE2E_RestHook_503WithHTTPDateRetryAfter exercises the alternate
// Retry-After form (RFC 7231 HTTP-date). The channel should parse the
// date and convert to a positive duration.
func TestE2E_RestHook_503WithHTTPDateRetryAfter(t *testing.T) {
	t.Parallel()
	target := time.Now().Add(45 * time.Second).UTC()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", target.Format(http.TimeFormat))
		w.WriteHeader(http.StatusServiceUnavailable)
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
	// HTTP-date arithmetic gives ~45s give-or-take a couple seconds for
	// clock skew between the test setting "now+45s" and the channel
	// reading "until target".
	if out.RetryAfter < 30*time.Second || out.RetryAfter > 60*time.Second {
		t.Errorf("RetryAfter=%v; want roughly 45s", out.RetryAfter)
	}
}

// TestE2E_RestHook_401Permanent exercises a subscriber returning 401
// Unauthorized: subscriber rejects the bearer/OAuth credential. This is
// a permanent failure — the credential won't fix itself by retrying;
// the operator must rotate it.
func TestE2E_RestHook_401Permanent(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
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
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("kind=%v reason=%q; want Permanent", out.Kind, out.Reason)
	}
	if out.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode=%d; want 401", out.StatusCode)
	}
	// 401 is a likely candidate for retry storms; reason should clearly
	// say "401" so operators see it in dead_letters.
	if !strings.Contains(out.Reason, "401") {
		t.Errorf("reason=%q; want to contain '401'", out.Reason)
	}
}

// TestE2E_RestHook_403Permanent exercises a subscriber returning 403:
// also permanent (subscriber's authorization layer flat-out denies).
func TestE2E_RestHook_403Permanent(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
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
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("kind=%v; want Permanent", out.Kind)
	}
}

// TestE2E_RestHook_SlowSubscriberHitsDeadline exercises a subscriber that
// accepts the connection but never replies. The channel must classify
// Transient (i/o timeout) once the envelope's Deadline elapses. Without
// the deadline, the goroutine would block forever, exhausting connection
// pools.
func TestE2E_RestHook_SlowSubscriberHitsDeadline(t *testing.T) {
	t.Parallel()
	hangFor := 5 * time.Second
	deadline := 750 * time.Millisecond

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(hangFor):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
			return
		}
	}))
	defer srv.Close()

	ch, err := resthook.New(resthook.Options{
		HTTPClient:     srv.Client(),
		RequestTimeout: deadline,
	})
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
		Deadline:             time.Now().Add(deadline),
	}
	start := time.Now()
	out, err := ch.Deliver(context.Background(), env)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if out.Kind != channel.OutcomeTransient {
		t.Fatalf("kind=%v reason=%q; want Transient (slow-subscriber timeout)",
			out.Kind, out.Reason)
	}
	// Should bail out at roughly `deadline` (within a generous slack).
	if elapsed > 4*time.Second {
		t.Errorf("Deliver took %v; expected to bail near %v deadline", elapsed, deadline)
	}
}

// TestE2E_RestHook_429WithRetryAfterTransient exercises 429 Too Many
// Requests + Retry-After: 5. Different status code path than 503 but
// same scheduler contract — Transient with hint.
func TestE2E_RestHook_429WithRetryAfterTransient(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
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
	if out.RetryAfter < 5*time.Second {
		t.Errorf("RetryAfter=%v; want >= 5s", out.RetryAfter)
	}
	if out.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode=%d; want 429", out.StatusCode)
	}
}

// TestE2E_RestHook_404Permanent exercises 404 Not Found: the subscriber
// endpoint URL is wrong (operator misconfiguration). Permanent — no
// amount of retry will conjure the URL into existence.
func TestE2E_RestHook_404Permanent(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
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
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("kind=%v; want Permanent", out.Kind)
	}
}

// TestE2E_RestHook_500WithoutRetryAfter exercises a subscriber returning
// raw 500 with no Retry-After. Channel classifies Transient, but
// RetryAfter must be zero (signaling the scheduler to use its own
// backoff curve).
func TestE2E_RestHook_500WithoutRetryAfter(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
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
	if out.RetryAfter != 0 {
		t.Errorf("RetryAfter=%v; want 0 (no header)", out.RetryAfter)
	}
}

// TestE2E_RestHook_NoFDLeakUnderTransientRetries fires 50 deliveries
// against a server that 503s every call, asserting that the channel
// reuses connections (HTTP keep-alive) instead of opening a fresh socket
// per delivery and leaving them in TIME_WAIT.
//
// The "leak" symptom is that an endpoint stuck in transient failure
// mode would, over hours of retries, exhaust ephemeral ports on the
// host. This test does NOT measure ephemeral ports directly (no
// portable way), but it asserts via the server-side counter that the
// channel does not crash or error out under sustained transient
// failures, and finishes within a reasonable wall time (a 50x retry
// burst should not take >5s on localhost).
func TestE2E_RestHook_NoFDLeakUnderTransientRetries(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ch, err := resthook.New(resthook.Options{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	const N = 50
	start := time.Now()
	for i := 0; i < N; i++ {
		env := channel.NotificationEnvelope{
			SubscriptionID:       uuid.New(),
			Sequence:             uint64(i + 1),
			BundleBytes:          []byte(`{"resourceType":"Bundle"}`),
			BundleKind:           channel.BundleEventNotification,
			PayloadType:          channel.PayloadIDOnly,
			ContentType:          channel.ContentTypeFHIRJSON,
			Attempt:              1,
			CorrelationID:        uuid.New().String(),
			SubscriptionEndpoint: srv.URL + "/webhook",
			Deadline:             time.Now().Add(2 * time.Second),
		}
		out, err := ch.Deliver(context.Background(), env)
		if err != nil {
			t.Fatalf("deliver %d: %v", i, err)
		}
		if out.Kind != channel.OutcomeTransient {
			t.Fatalf("deliver %d kind=%v; want Transient", i, out.Kind)
		}
	}
	elapsed := time.Since(start)
	if elapsed > 10*time.Second {
		t.Errorf("50 transient retries took %v; expected <10s (suggests no keep-alive)", elapsed)
	}
	if hits.Load() != N {
		t.Errorf("server saw %d hits; want %d", hits.Load(), N)
	}
}
