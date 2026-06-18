// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package resthook_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/resthook"
)

// recordingReceiver is an httptest-based mock subscriber that records every
// POST it receives so the test can assert the full request shape.
type recordingReceiver struct {
	mu       sync.Mutex
	requests []recordedRequest
	srv      *httptest.Server
	// status is the response status code to return; can be swapped per
	// request via the next() callback.
	next func(req int) (status int, retryAfter string, body []byte)
	// counter increments per request; used to drive next().
	counter atomic.Int64
}

type recordedRequest struct {
	method  string
	path    string
	headers http.Header
	body    []byte
}

func newRecordingReceiver() *recordingReceiver {
	r := &recordingReceiver{}
	r.srv = httptest.NewTLSServer(http.HandlerFunc(r.serve))
	return r
}

func (r *recordingReceiver) close()               { r.srv.Close() }
func (r *recordingReceiver) url() string          { return r.srv.URL }
func (r *recordingReceiver) client() *http.Client { return r.srv.Client() }

func (r *recordingReceiver) serve(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.requests = append(r.requests, recordedRequest{
		method:  req.Method,
		path:    req.URL.Path,
		headers: req.Header.Clone(),
		body:    body,
	})
	r.mu.Unlock()

	idx := r.counter.Add(1)
	status := http.StatusOK
	var ra string
	var respBody []byte
	if r.next != nil {
		status, ra, respBody = r.next(int(idx))
	}
	if ra != "" {
		w.Header().Set("Retry-After", ra)
	}
	w.WriteHeader(status)
	if len(respBody) > 0 {
		_, _ = w.Write(respBody)
	}
}

func (r *recordingReceiver) snapshot() []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedRequest, len(r.requests))
	copy(out, r.requests)
	return out
}

// TestIntegrationFullEHRReceiverHappyPath simulates a real subscriber:
// HTTPS endpoint, echoes back required FHIR headers, asserts the channel
// produced the right wire shape end-to-end.
func TestIntegrationFullEHRReceiverHappyPath(t *testing.T) {
	t.Parallel()

	rcv := newRecordingReceiver()
	defer rcv.close()

	bundle := []byte(`{"resourceType":"Bundle","type":"subscription-notification","entry":[{"resource":{"resourceType":"SubscriptionStatus"}}]}`)

	ch, err := resthook.New(resthook.Options{
		HTTPClient: rcv.client(),
		Metrics:    newFakeMetrics(),
		UserAgent:  "fhir-subs-it/1.0",
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	subID := uuid.New()
	corr := uuid.NewString()
	env := channel.NotificationEnvelope{
		SubscriptionID:       subID,
		Sequence:             100,
		BundleBytes:          bundle,
		BundleKind:           channel.BundleEventNotification,
		PayloadType:          channel.PayloadFullResource,
		ContentType:          channel.ContentTypeFHIRJSON,
		Attempt:              2,
		CorrelationID:        corr,
		SubscriptionEndpoint: rcv.url() + "/notify",
		SubscriptionParameters: []channel.Param{
			{Name: "X-Tenant", Value: "memorial-east"},
			{Name: "If-Match", Value: `W/"v3"`},
			{Name: "Authorization", Value: "Bearer leaked"}, // must be filtered
		},
		Deadline: time.Now().Add(5 * time.Second),
	}

	out, err := ch.Deliver(context.Background(), env)
	if err != nil {
		t.Fatalf("deliver err: %v", err)
	}
	if out.Kind != channel.OutcomeDelivered {
		t.Fatalf("expected Delivered; got %v reason=%q", out.Kind, out.Reason)
	}
	if out.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d", out.StatusCode)
	}

	reqs := rcv.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request; got %d", len(reqs))
	}
	r := reqs[0]
	if r.method != http.MethodPost {
		t.Errorf("method = %q", r.method)
	}
	if r.path != "/notify" {
		t.Errorf("path = %q", r.path)
	}
	// Server-injected headers.
	if got := r.headers.Get("Content-Type"); got != "application/fhir+json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := r.headers.Get("User-Agent"); got != "fhir-subs-it/1.0" {
		t.Errorf("User-Agent = %q", got)
	}
	if got := r.headers.Get("X-Subscription-Id"); got != subID.String() {
		t.Errorf("X-Subscription-Id = %q", got)
	}
	if got := r.headers.Get("X-Subscription-Event-Number"); got != "100" {
		t.Errorf("X-Subscription-Event-Number = %q", got)
	}
	if got := r.headers.Get("X-Attempt"); got != "2" {
		t.Errorf("X-Attempt = %q", got)
	}
	if got := r.headers.Get("traceparent"); got == "" || !strings.HasPrefix(got, "00-") {
		t.Errorf("traceparent malformed = %q", got)
	}
	if got := r.headers.Get("X-Correlation-ID"); got != corr {
		t.Errorf("X-Correlation-ID = %q", got)
	}
	// Subscriber-supplied headers: allowed pass through; denied dropped.
	if got := r.headers.Get("X-Tenant"); got != "memorial-east" {
		t.Errorf("X-Tenant = %q", got)
	}
	if got := r.headers.Get("If-Match"); got != `W/"v3"` {
		t.Errorf("If-Match = %q", got)
	}
	if got := r.headers.Get("Authorization"); got != "" {
		t.Errorf("Authorization leaked = %q", got)
	}
	// Body fidelity: bytes go through unchanged.
	if string(r.body) != string(bundle) {
		t.Errorf("body mismatch")
	}
	// And the body parses as JSON Bundle for sanity.
	var b map[string]any
	if err := json.Unmarshal(r.body, &b); err != nil {
		t.Errorf("body not valid JSON: %v", err)
	}
	if b["resourceType"] != "Bundle" {
		t.Errorf("resourceType = %v", b["resourceType"])
	}
}

// TestIntegrationRetryAfterRespectedThroughClassification verifies that
// the channel pulls Retry-After off a 503 response and surfaces it in the
// outcome unchanged.
func TestIntegrationRetryAfterRespectedThroughClassification(t *testing.T) {
	t.Parallel()

	rcv := newRecordingReceiver()
	defer rcv.close()
	rcv.next = func(int) (int, string, []byte) {
		return http.StatusServiceUnavailable, "60", []byte(`{"error":"unavailable"}`)
	}

	ch, _ := resthook.New(resthook.Options{HTTPClient: rcv.client(), Metrics: newFakeMetrics()})
	out, _ := ch.Deliver(context.Background(), newEnvelope(rcv.url()))
	if out.Kind != channel.OutcomeTransient {
		t.Fatalf("expected Transient; got %v: %q", out.Kind, out.Reason)
	}
	if out.RetryAfter != 60*time.Second {
		t.Errorf("RetryAfter = %v, want 60s", out.RetryAfter)
	}
	if out.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("StatusCode = %d", out.StatusCode)
	}
}

// TestIntegrationPermanentBodyExcerptIncluded verifies a 4xx response body
// shows up (truncated) in the Reason field.
func TestIntegrationPermanentBodyExcerptIncluded(t *testing.T) {
	t.Parallel()

	rcv := newRecordingReceiver()
	defer rcv.close()
	rcv.next = func(int) (int, string, []byte) {
		// OperationOutcome-shaped body, well within the 256-byte cap.
		body := []byte(`{"resourceType":"OperationOutcome","issue":[{"severity":"error","code":"forbidden","diagnostics":"client not authorized for topic"}]}`)
		return http.StatusForbidden, "", body
	}

	ch, _ := resthook.New(resthook.Options{HTTPClient: rcv.client(), Metrics: newFakeMetrics()})
	out, _ := ch.Deliver(context.Background(), newEnvelope(rcv.url()))
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("expected Permanent; got %v", out.Kind)
	}
	if !strings.Contains(out.Reason, "403") {
		t.Errorf("reason missing status: %q", out.Reason)
	}
	if !strings.Contains(out.Reason, "OperationOutcome") {
		t.Errorf("reason missing body excerpt: %q", out.Reason)
	}
}

// TestIntegrationConcurrentDeliveriesShareClient verifies the channel is
// safe to call concurrently from many goroutines (the scheduler will).
func TestIntegrationConcurrentDeliveriesShareClient(t *testing.T) {
	t.Parallel()

	rcv := newRecordingReceiver()
	defer rcv.close()

	ch, _ := resthook.New(resthook.Options{HTTPClient: rcv.client(), Metrics: newFakeMetrics()})

	const N = 32
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			env := newEnvelope(rcv.url())
			out, err := ch.Deliver(context.Background(), env)
			if err != nil {
				t.Errorf("deliver: %v", err)
			}
			if out.Kind != channel.OutcomeDelivered {
				t.Errorf("kind = %v", out.Kind)
			}
		}()
	}
	wg.Wait()
	if got := rcv.counter.Load(); got != N {
		t.Errorf("server saw %d requests, want %d", got, N)
	}
}

// TestIntegrationHeartbeatBundleKindOmitsEventHeader verifies a heartbeat
// envelope does NOT carry the X-Subscription-Event-Number header (it's
// only set for event-notification kind).
func TestIntegrationHeartbeatBundleKindOmitsEventHeader(t *testing.T) {
	t.Parallel()

	rcv := newRecordingReceiver()
	defer rcv.close()

	ch, _ := resthook.New(resthook.Options{HTTPClient: rcv.client(), Metrics: newFakeMetrics()})
	env := newEnvelope(rcv.url())
	env.BundleKind = channel.BundleHeartbeat
	out, _ := ch.Deliver(context.Background(), env)
	requireDelivered(t, out)

	reqs := rcv.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	if got := reqs[0].headers.Get("X-Subscription-Event-Number"); got != "" {
		t.Errorf("X-Subscription-Event-Number leaked on heartbeat = %q", got)
	}
}
