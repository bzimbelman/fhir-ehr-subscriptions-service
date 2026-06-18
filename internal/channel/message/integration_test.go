// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package message_test

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
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/message"
)

type recordingMessagingReceiver struct {
	mu       sync.Mutex
	requests []recordedRequest
	srv      *httptest.Server
	next     func(req int) (status int, retryAfter string, body []byte)
	counter  atomic.Int64
}

type recordedRequest struct {
	method  string
	path    string
	headers http.Header
	body    []byte
}

func newRecordingMessagingReceiver(useTLS bool) *recordingMessagingReceiver {
	r := &recordingMessagingReceiver{}
	if useTLS {
		r.srv = httptest.NewTLSServer(http.HandlerFunc(r.serve))
	} else {
		r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
	}
	return r
}

func (r *recordingMessagingReceiver) close()               { r.srv.Close() }
func (r *recordingMessagingReceiver) url() string          { return r.srv.URL }
func (r *recordingMessagingReceiver) client() *http.Client { return r.srv.Client() }

func (r *recordingMessagingReceiver) serve(w http.ResponseWriter, req *http.Request) {
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

func (r *recordingMessagingReceiver) snapshot() []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedRequest, len(r.requests))
	copy(out, r.requests)
	return out
}

// TestIntegrationMessagingFullEndpointReceivesWrappedBundle verifies the
// full wire shape end-to-end: outer Bundle is type=message, MessageHeader
// is the first entry, and the original SubscriptionStatus is also present.
func TestIntegrationMessagingFullEndpointReceivesWrappedBundle(t *testing.T) {
	t.Parallel()

	rcv := newRecordingMessagingReceiver(true)
	defer rcv.close()

	ch, err := message.New(message.Options{
		HTTPClient:     rcv.client(),
		Metrics:        newFakeMetrics(),
		UserAgent:      "fhir-subs-msg-it/1.0",
		ServerEndpoint: "https://fhir-subs.example.org/facility/it-test",
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	subID := uuid.New()
	corr := uuid.NewString()
	env := channel.NotificationEnvelope{
		SubscriptionID:       subID,
		Sequence:             100,
		BundleBytes:          validInnerBundle(t, "event-notification"),
		BundleKind:           channel.BundleEventNotification,
		PayloadType:          channel.PayloadFullResource,
		ContentType:          channel.ContentTypeFHIRJSON,
		Attempt:              2,
		CorrelationID:        corr,
		SubscriptionEndpoint: rcv.url() + "/messaging",
		Deadline:             time.Now().Add(5 * time.Second),
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
	if r.path != "/messaging" {
		t.Errorf("path = %q", r.path)
	}
	if got := r.headers.Get("Content-Type"); got != "application/fhir+json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := r.headers.Get("X-Subscription-Id"); got != subID.String() {
		t.Errorf("X-Subscription-Id = %q", got)
	}
	if got := r.headers.Get("X-Correlation-ID"); got != corr {
		t.Errorf("X-Correlation-ID = %q", got)
	}

	// Outer Bundle assertions.
	var outer map[string]any
	if err := json.Unmarshal(r.body, &outer); err != nil {
		t.Fatalf("outer parse: %v: %s", err, string(r.body))
	}
	if outer["resourceType"] != "Bundle" {
		t.Errorf("outer.resourceType = %v", outer["resourceType"])
	}
	if outer["type"] != "message" {
		t.Errorf("outer.type = %v, want message", outer["type"])
	}
	entries, ok := outer["entry"].([]any)
	if !ok || len(entries) < 2 {
		t.Fatalf("expected >=2 outer entries; got %v", outer["entry"])
	}
	mh := entries[0].(map[string]any)["resource"].(map[string]any)
	if mh["resourceType"] != "MessageHeader" {
		t.Errorf("entry[0].resource = %v, want MessageHeader", mh["resourceType"])
	}
	// MessageHeader.eventCoding per ADR 0008 #13.
	ec := mh["eventCoding"].(map[string]any)
	if ec["system"] != "http://terminology.hl7.org/CodeSystem/subscription-notification-type" {
		t.Errorf("eventCoding.system = %v", ec["system"])
	}
	if ec["code"] != "event-notification" {
		t.Errorf("eventCoding.code = %v", ec["code"])
	}
	// destination[0].endpoint
	dest := mh["destination"].([]any)
	d0 := dest[0].(map[string]any)
	if d0["endpoint"] != env.SubscriptionEndpoint {
		t.Errorf("destination[0].endpoint = %v", d0["endpoint"])
	}
	// source.endpoint
	src := mh["source"].(map[string]any)
	if src["endpoint"] != "https://fhir-subs.example.org/facility/it-test" {
		t.Errorf("source.endpoint = %v", src["endpoint"])
	}

	// The inner SubscriptionStatus must be present after the MessageHeader.
	foundStatus := false
	for i := 1; i < len(entries); i++ {
		entry, ok := entries[i].(map[string]any)
		if !ok {
			continue
		}
		res, ok := entry["resource"].(map[string]any)
		if !ok {
			continue
		}
		if res["resourceType"] == "SubscriptionStatus" {
			foundStatus = true
			break
		}
	}
	if !foundStatus {
		t.Errorf("inner SubscriptionStatus not present after MessageHeader; entries=%v", entries)
	}
}

func TestIntegrationMessaging5xxRetryAfterPropagated(t *testing.T) {
	t.Parallel()
	rcv := newRecordingMessagingReceiver(true)
	defer rcv.close()
	rcv.next = func(int) (int, string, []byte) {
		return http.StatusServiceUnavailable, "60", nil
	}
	ch, _ := message.New(message.Options{HTTPClient: rcv.client(), Metrics: newFakeMetrics()})
	env := newEnvelope(t, rcv.url())
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomeTransient {
		t.Fatalf("expected Transient on 5xx, got %v: %q", out.Kind, out.Reason)
	}
	if out.RetryAfter != 60*time.Second {
		t.Errorf("RetryAfter = %v, want 60s", out.RetryAfter)
	}
}

func TestIntegrationMessagingTLSHappyPath(t *testing.T) {
	t.Parallel()
	// Explicitly exercises httptest.NewTLSServer (TLS handshake succeeds with
	// the test cert injected via srv.Client()).
	rcv := newRecordingMessagingReceiver(true)
	defer rcv.close()
	ch, _ := message.New(message.Options{HTTPClient: rcv.client(), Metrics: newFakeMetrics()})
	out, err := ch.Deliver(context.Background(), newEnvelope(t, rcv.url()))
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if out.Kind != channel.OutcomeDelivered {
		t.Fatalf("expected Delivered over TLS, got %v: %q", out.Kind, out.Reason)
	}
}

// TestIntegrationMessagingConcurrentDeliveriesShareClient verifies the channel
// is safe to call concurrently from many goroutines.
func TestIntegrationMessagingConcurrentDeliveriesShareClient(t *testing.T) {
	t.Parallel()
	rcv := newRecordingMessagingReceiver(true)
	defer rcv.close()

	ch, _ := message.New(message.Options{HTTPClient: rcv.client(), Metrics: newFakeMetrics()})

	const N = 32
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			env := newEnvelope(t, rcv.url())
			out, err := ch.Deliver(context.Background(), env)
			if err != nil {
				t.Errorf("deliver: %v", err)
			}
			if out.Kind != channel.OutcomeDelivered {
				t.Errorf("kind = %v reason=%q", out.Kind, out.Reason)
			}
		}()
	}
	wg.Wait()
	if got := rcv.counter.Load(); got != N {
		t.Errorf("server saw %d requests, want %d", got, N)
	}

	// Spot-check that the shape held under load: pick the first request
	// recorded and confirm the outer Bundle is well-formed.
	reqs := rcv.snapshot()
	if len(reqs) == 0 {
		t.Fatal("no recorded requests")
	}
	var outer map[string]any
	if err := json.Unmarshal(reqs[0].body, &outer); err != nil {
		t.Fatalf("body parse: %v", err)
	}
	if outer["type"] != "message" {
		t.Errorf("outer type under load = %v", outer["type"])
	}
}

// TestIntegrationMessagingHeaderAllowlistEndToEnd asserts the same allowlist
// behavior as the unit test but observed across the real HTTP boundary.
func TestIntegrationMessagingHeaderAllowlistEndToEnd(t *testing.T) {
	t.Parallel()
	rcv := newRecordingMessagingReceiver(true)
	defer rcv.close()

	ch, _ := message.New(message.Options{HTTPClient: rcv.client(), Metrics: newFakeMetrics()})
	env := newEnvelope(t, rcv.url())
	env.SubscriptionParameters = []channel.Param{
		{Name: "Authorization", Value: "Bearer leaked"}, // deny: never echo
		{Name: "X-Real-IP", Value: "10.0.0.5"},          // deny: reserved prefix
		{Name: "X-Tenant", Value: "memorial-east"},      // allow: custom
		{Name: "If-None-Match", Value: `W/"v3"`},        // allow: FHIR list
	}
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomeDelivered {
		t.Fatalf("expected Delivered, got %v", out.Kind)
	}

	reqs := rcv.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	r := reqs[0]
	if got := r.headers.Get("Authorization"); got != "" {
		t.Errorf("Authorization leaked: %q", got)
	}
	if got := r.headers.Get("X-Real-IP"); got != "" {
		t.Errorf("X-Real-IP leaked: %q", got)
	}
	if got := r.headers.Get("X-Tenant"); got != "memorial-east" {
		t.Errorf("X-Tenant = %q", got)
	}
	if got := r.headers.Get("If-None-Match"); got != strings.TrimSpace(`W/"v3"`) {
		t.Errorf("If-None-Match = %q", got)
	}
}
