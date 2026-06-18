// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package message_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/message"
)

// fakeMetrics records counter and histogram calls so the tests can
// assert that the channel emits the documented metric names.
type fakeMetrics struct {
	mu       sync.Mutex
	counters map[string]float64
	observed map[string][]float64
}

func newFakeMetrics() *fakeMetrics {
	return &fakeMetrics{
		counters: make(map[string]float64),
		observed: make(map[string][]float64),
	}
}

func (f *fakeMetrics) Inc(name string, labels map[string]string) { f.Add(name, 1, labels) }

func (f *fakeMetrics) Add(name string, delta float64, labels map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counters[keyFor(name, labels)] += delta
}

func (f *fakeMetrics) Observe(name string, value float64, labels map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed[keyFor(name, labels)] = append(f.observed[keyFor(name, labels)], value)
}

func (f *fakeMetrics) Set(string, float64, map[string]string) {}

func (f *fakeMetrics) get(name string, labels map[string]string) float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counters[keyFor(name, labels)]
}

func (f *fakeMetrics) observeCount(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for k, v := range f.observed {
		if strings.HasPrefix(k, name) {
			total += len(v)
		}
	}
	return total
}

func keyFor(name string, labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	var b strings.Builder
	b.WriteString(name)
	for _, k := range keys {
		b.WriteString("|")
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(labels[k])
	}
	return b.String()
}

// validInnerBundle returns a serialized subscription-notification Bundle
// whose first entry is a SubscriptionStatus with the named type.
func validInnerBundle(t *testing.T, statusType string) []byte {
	t.Helper()
	inner := map[string]any{
		"resourceType": "Bundle",
		"id":           "inner-" + statusType,
		"type":         "subscription-notification",
		"entry": []map[string]any{
			{
				"resource": map[string]any{
					"resourceType": "SubscriptionStatus",
					"type":         statusType,
				},
			},
		},
	}
	b, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("marshal inner: %v", err)
	}
	return b
}

func newEnvelope(t *testing.T, endpoint string) channel.NotificationEnvelope {
	return channel.NotificationEnvelope{
		SubscriptionID:       uuid.New(),
		Sequence:             7,
		BundleBytes:          validInnerBundle(t, "event-notification"),
		BundleKind:           channel.BundleEventNotification,
		PayloadType:          channel.PayloadIDOnly,
		ContentType:          channel.ContentTypeFHIRJSON,
		Attempt:              1,
		CorrelationID:        uuid.New().String(),
		SubscriptionEndpoint: endpoint,
		Deadline:             time.Now().Add(10 * time.Second),
	}
}

func requireDelivered(t *testing.T, o channel.DeliveryOutcome) {
	t.Helper()
	if o.Kind != channel.OutcomeDelivered {
		t.Fatalf("expected Delivered, got %v: %q", o.Kind, o.Reason)
	}
}

// TestSuccessfulPOSTWrapsBundleAsMessage verifies the happy path: the
// channel parses the inner Bundle, wraps it in a Bundle.type=message with
// a MessageHeader at entry[0], and POSTs the result.
func TestSuccessfulPOSTWrapsBundleAsMessage(t *testing.T) {
	t.Parallel()

	var (
		gotContentType string
		gotMethod      string
		gotPath        string
		gotBody        []byte
	)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch, err := message.New(message.Options{
		HTTPClient:     srv.Client(),
		Metrics:        newFakeMetrics(),
		ServerEndpoint: "https://fhir-subs.example.org/facility/memorial-east",
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	env := newEnvelope(t, srv.URL+"/messaging")
	out, err := ch.Deliver(context.Background(), env)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	requireDelivered(t, out)

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q", gotMethod)
	}
	if gotPath != "/messaging" {
		t.Errorf("path = %q", gotPath)
	}
	if gotContentType != "application/fhir+json" {
		t.Errorf("Content-Type = %q", gotContentType)
	}
	var outer map[string]any
	if err := json.Unmarshal(gotBody, &outer); err != nil {
		t.Fatalf("outer body not valid JSON: %v\nbody=%s", err, string(gotBody))
	}
	if outer["resourceType"] != "Bundle" {
		t.Errorf("outer resourceType = %v", outer["resourceType"])
	}
	if outer["type"] != "message" {
		t.Errorf("outer type = %v, want message", outer["type"])
	}
	entries, ok := outer["entry"].([]any)
	if !ok || len(entries) < 2 {
		t.Fatalf("expected >=2 entries, got %v", outer["entry"])
	}
	first, ok := entries[0].(map[string]any)
	if !ok {
		t.Fatalf("entry[0] not an object: %v", entries[0])
	}
	res, ok := first["resource"].(map[string]any)
	if !ok {
		t.Fatalf("entry[0].resource not an object: %v", first["resource"])
	}
	if res["resourceType"] != "MessageHeader" {
		t.Errorf("entry[0].resource.resourceType = %v, want MessageHeader", res["resourceType"])
	}
	// MessageHeader.eventCoding per ADR 0008 #13.
	ec, ok := res["eventCoding"].(map[string]any)
	if !ok {
		t.Fatalf("eventCoding missing or wrong shape: %v", res["eventCoding"])
	}
	if ec["system"] != "http://terminology.hl7.org/CodeSystem/subscription-notification-type" {
		t.Errorf("eventCoding.system = %v", ec["system"])
	}
	if ec["code"] != "event-notification" {
		t.Errorf("eventCoding.code = %v, want event-notification", ec["code"])
	}
	// destination[0].endpoint
	dest, ok := res["destination"].([]any)
	if !ok || len(dest) == 0 {
		t.Fatalf("destination missing: %v", res["destination"])
	}
	d0, _ := dest[0].(map[string]any)
	if d0["endpoint"] != env.SubscriptionEndpoint {
		t.Errorf("destination[0].endpoint = %v, want %s", d0["endpoint"], env.SubscriptionEndpoint)
	}
	// source.endpoint
	src, ok := res["source"].(map[string]any)
	if !ok {
		t.Fatalf("source missing")
	}
	if src["endpoint"] != "https://fhir-subs.example.org/facility/memorial-east" {
		t.Errorf("source.endpoint = %v", src["endpoint"])
	}
	// focus references the inner Bundle.
	focus, ok := res["focus"].([]any)
	if !ok || len(focus) == 0 {
		t.Fatalf("focus missing")
	}
	f0, _ := focus[0].(map[string]any)
	ref, _ := f0["reference"].(string)
	if !strings.HasPrefix(ref, "Bundle/") && !strings.HasPrefix(ref, "urn:uuid:") {
		t.Errorf("focus[0].reference = %q, want Bundle/* or urn:uuid:*", ref)
	}
}

func Test4xxIsPermanentFailure(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"resourceType":"OperationOutcome"}`))
	}))
	defer srv.Close()

	ch, _ := message.New(message.Options{HTTPClient: srv.Client(), Metrics: newFakeMetrics()})
	out, _ := ch.Deliver(context.Background(), newEnvelope(t, srv.URL))
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("expected Permanent for 4xx, got %v: %q", out.Kind, out.Reason)
	}
	if !strings.Contains(out.Reason, "400") {
		t.Errorf("reason missing status: %q", out.Reason)
	}
}

func Test5xxIsTransientFailure(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	ch, _ := message.New(message.Options{HTTPClient: srv.Client(), Metrics: newFakeMetrics()})
	out, _ := ch.Deliver(context.Background(), newEnvelope(t, srv.URL))
	if out.Kind != channel.OutcomeTransient {
		t.Fatalf("expected Transient for 5xx, got %v: %q", out.Kind, out.Reason)
	}
	if out.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s", out.RetryAfter)
	}
}

func TestNetworkErrorTransient(t *testing.T) {
	t.Parallel()
	// Bind on an ephemeral port and immediately close to guarantee dial-refused.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	ch, _ := message.New(message.Options{Metrics: newFakeMetrics()})
	env := newEnvelope(t, "https://"+addr+"/messaging")
	env.Deadline = time.Now().Add(2 * time.Second)
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomeTransient {
		t.Fatalf("expected Transient on dial refused, got %v: %q", out.Kind, out.Reason)
	}
}

func TestTimeoutTransient(t *testing.T) {
	t.Parallel()

	stop := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-stop:
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(func() {
		close(stop)
		srv.Close()
	})

	ch, _ := message.New(message.Options{HTTPClient: srv.Client(), Metrics: newFakeMetrics()})
	env := newEnvelope(t, srv.URL)
	env.Deadline = time.Now().Add(150 * time.Millisecond)
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomeTransient {
		t.Fatalf("expected Transient on timeout, got %v: %q", out.Kind, out.Reason)
	}
}

func TestNonHTTPSEndpointPermanentFailure(t *testing.T) {
	t.Parallel()
	ch, _ := message.New(message.Options{Metrics: newFakeMetrics()})
	env := newEnvelope(t, "http://insecure.example/messaging")
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("expected Permanent for non-https, got %v: %q", out.Kind, out.Reason)
	}
}

func TestInnerBundleParseFailureIsPermanent(t *testing.T) {
	t.Parallel()
	// LLD §10 "Inner Bundle parse failure | PermanentFailure".
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch, _ := message.New(message.Options{HTTPClient: srv.Client(), Metrics: newFakeMetrics()})
	env := newEnvelope(t, srv.URL)
	env.BundleBytes = []byte(`{not-json`)
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("expected Permanent on inner-bundle parse failure, got %v: %q", out.Kind, out.Reason)
	}
}

func TestInnerBundleMissingSubscriptionStatusIsPermanent(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch, _ := message.New(message.Options{HTTPClient: srv.Client(), Metrics: newFakeMetrics()})
	env := newEnvelope(t, srv.URL)
	// Bundle with no entries — MessageHeader.eventCoding can't be derived.
	env.BundleBytes = []byte(`{"resourceType":"Bundle","type":"subscription-notification","entry":[]}`)
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("expected Permanent on missing SubscriptionStatus, got %v: %q", out.Kind, out.Reason)
	}
}

func TestHeaderAllowlistFiltersDenyListed(t *testing.T) {
	t.Parallel()
	var (
		gotAuth     string
		gotXForward string
		gotCustom   string
		gotIfMatch  string
	)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotXForward = r.Header.Get("X-Forwarded-For")
		gotCustom = r.Header.Get("X-Tenant")
		gotIfMatch = r.Header.Get("If-Match")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch, _ := message.New(message.Options{HTTPClient: srv.Client(), Metrics: newFakeMetrics()})

	env := newEnvelope(t, srv.URL)
	env.SubscriptionParameters = []channel.Param{
		{Name: "Authorization", Value: "Bearer hunter2"}, // deny: never echo
		{Name: "X-Forwarded-For", Value: "10.0.0.1"},     // deny: reserved prefix
		{Name: "X-Tenant", Value: "acme"},                // allow: custom
		{Name: "If-Match", Value: `W/"abc"`},             // allow: FHIR list
	}
	out, _ := ch.Deliver(context.Background(), env)
	requireDelivered(t, out)

	if gotAuth != "" {
		t.Errorf("Authorization leaked: %q", gotAuth)
	}
	if gotXForward != "" {
		t.Errorf("X-Forwarded-For leaked: %q", gotXForward)
	}
	if gotCustom != "acme" {
		t.Errorf("X-Tenant header dropped: %q", gotCustom)
	}
	if gotIfMatch != `W/"abc"` {
		t.Errorf("If-Match dropped: %q", gotIfMatch)
	}
}

func TestServerInjectedHeadersPresent(t *testing.T) {
	t.Parallel()
	var (
		gotXSubID    string
		gotXAttempt  string
		gotXEventNum string
		gotTracep    string
		gotUserAgent string
	)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotXSubID = r.Header.Get("X-Subscription-Id")
		gotXAttempt = r.Header.Get("X-Attempt")
		gotXEventNum = r.Header.Get("X-Subscription-Event-Number")
		gotTracep = r.Header.Get("traceparent")
		gotUserAgent = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch, _ := message.New(message.Options{
		HTTPClient: srv.Client(),
		Metrics:    newFakeMetrics(),
		UserAgent:  "fhir-subs-msg-test/1.0",
	})
	env := newEnvelope(t, srv.URL)
	out, _ := ch.Deliver(context.Background(), env)
	requireDelivered(t, out)

	if gotUserAgent != "fhir-subs-msg-test/1.0" {
		t.Errorf("User-Agent = %q", gotUserAgent)
	}
	if gotXSubID != env.SubscriptionID.String() {
		t.Errorf("X-Subscription-Id = %q", gotXSubID)
	}
	if gotXAttempt != "1" {
		t.Errorf("X-Attempt = %q", gotXAttempt)
	}
	if gotXEventNum != "7" {
		t.Errorf("X-Subscription-Event-Number = %q", gotXEventNum)
	}
	if !strings.HasPrefix(gotTracep, "00-") {
		t.Errorf("traceparent malformed: %q", gotTracep)
	}
}

func TestHeartbeatBundleKindOmitsEventNumber(t *testing.T) {
	t.Parallel()
	var got string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Subscription-Event-Number")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch, _ := message.New(message.Options{HTTPClient: srv.Client(), Metrics: newFakeMetrics()})
	env := newEnvelope(t, srv.URL)
	env.BundleKind = channel.BundleHeartbeat
	env.BundleBytes = validInnerBundle(t, "heartbeat")
	out, _ := ch.Deliver(context.Background(), env)
	requireDelivered(t, out)
	if got != "" {
		t.Errorf("X-Subscription-Event-Number leaked on heartbeat = %q", got)
	}
}

func TestHeartbeatEventCodingMatchesStatusType(t *testing.T) {
	t.Parallel()
	// Verifies eventCoding.code reflects the inner SubscriptionStatus.type
	// per ADR 0008 #13. For a heartbeat the code must be "heartbeat".
	var gotBody []byte
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch, _ := message.New(message.Options{HTTPClient: srv.Client(), Metrics: newFakeMetrics()})
	env := newEnvelope(t, srv.URL)
	env.BundleKind = channel.BundleHeartbeat
	env.BundleBytes = validInnerBundle(t, "heartbeat")
	out, _ := ch.Deliver(context.Background(), env)
	requireDelivered(t, out)

	var outer map[string]any
	if err := json.Unmarshal(gotBody, &outer); err != nil {
		t.Fatalf("outer parse: %v", err)
	}
	entries := outer["entry"].([]any)
	res := entries[0].(map[string]any)["resource"].(map[string]any)
	ec := res["eventCoding"].(map[string]any)
	if ec["code"] != "heartbeat" {
		t.Errorf("heartbeat eventCoding.code = %v", ec["code"])
	}
}

func TestDeliveredMetricIncremented(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := newFakeMetrics()
	ch, _ := message.New(message.Options{HTTPClient: srv.Client(), Metrics: m})
	out, _ := ch.Deliver(context.Background(), newEnvelope(t, srv.URL))
	requireDelivered(t, out)

	if got := m.get(message.MetricDeliveriesTotal,
		map[string]string{"channel": "message", "outcome": "delivered"}); got != 1 {
		t.Errorf("expected 1 delivered counter, got %v (counters=%+v)", got, m.counters)
	}
	if got := m.observeCount(message.MetricWrappingDurationSec); got != 1 {
		t.Errorf("expected 1 wrapping_duration sample, got %v", got)
	}
}

func TestNewWithoutOptionsUsesDefaults(t *testing.T) {
	t.Parallel()
	ch, err := message.New(message.Options{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if ch == nil {
		t.Fatal("nil channel")
	}
}

// Compile-time check that *message.Channel satisfies channel.Channel.
var _ channel.Channel = (*message.Channel)(nil)
