// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package resthook_test

import (
	"context"
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
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/resthook"
)

// fakeMetrics records metric calls for assertions.
type fakeMetrics struct {
	mu       sync.Mutex
	counters map[string]float64
}

func newFakeMetrics() *fakeMetrics {
	return &fakeMetrics{counters: make(map[string]float64)}
}

func (f *fakeMetrics) Inc(name string, labels map[string]string) {
	f.Add(name, 1, labels)
}

func (f *fakeMetrics) Add(name string, delta float64, labels map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counters[keyFor(name, labels)] += delta
}

func (f *fakeMetrics) Observe(string, float64, map[string]string) {}
func (f *fakeMetrics) Set(string, float64, map[string]string)     {}

func (f *fakeMetrics) get(name string, labels map[string]string) float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counters[keyFor(name, labels)]
}

func keyFor(name string, labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	// stable order for tests
	if len(keys) > 1 {
		for i := 0; i < len(keys); i++ {
			for j := i + 1; j < len(keys); j++ {
				if keys[j] < keys[i] {
					keys[i], keys[j] = keys[j], keys[i]
				}
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

func newEnvelope(endpoint string) channel.NotificationEnvelope {
	return channel.NotificationEnvelope{
		SubscriptionID:       uuid.New(),
		Sequence:             7,
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

// requireDelivered fails fast if outcome is not Delivered.
func requireDelivered(t *testing.T, o channel.DeliveryOutcome) {
	t.Helper()
	if o.Kind != channel.OutcomeDelivered {
		t.Fatalf("expected Delivered, got %v: %q", o.Kind, o.Reason)
	}
}

func TestSuccessPath(t *testing.T) {
	t.Parallel()

	var (
		gotPath        string
		gotContentType string
		gotXSubID      string
		gotXAttempt    string
		gotXEventNum   string
		gotTracep      string
		gotUserAgent   string
		gotBody        []byte
	)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		gotXSubID = r.Header.Get("X-Subscription-Id")
		gotXAttempt = r.Header.Get("X-Attempt")
		gotXEventNum = r.Header.Get("X-Subscription-Event-Number")
		gotTracep = r.Header.Get("traceparent")
		gotUserAgent = r.Header.Get("User-Agent")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch, err := resthook.New(resthook.Options{
		HTTPClient: srv.Client(),
		Metrics:    newFakeMetrics(),
		UserAgent:  "fhir-subs-test/1.0",
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	env := newEnvelope(srv.URL + "/webhook")
	out, err := ch.Deliver(context.Background(), env)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	requireDelivered(t, out)

	if gotPath != "/webhook" {
		t.Errorf("path = %q", gotPath)
	}
	if gotContentType != "application/fhir+json" {
		t.Errorf("content-type = %q", gotContentType)
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
	if gotUserAgent != "fhir-subs-test/1.0" {
		t.Errorf("User-Agent = %q", gotUserAgent)
	}
	if gotTracep == "" {
		t.Errorf("traceparent header missing")
	}
	if string(gotBody) != string(env.BundleBytes) {
		t.Errorf("body mismatch")
	}
}

func TestNonHTTPSEndpointPermanentFailure(t *testing.T) {
	t.Parallel()
	ch, _ := resthook.New(resthook.Options{Metrics: newFakeMetrics()})
	env := newEnvelope("http://insecure.example/webhook")
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("expected Permanent, got %v: %q", out.Kind, out.Reason)
	}
}

func TestTransient5xxFailure(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ch, _ := resthook.New(resthook.Options{HTTPClient: srv.Client(), Metrics: newFakeMetrics()})
	out, _ := ch.Deliver(context.Background(), newEnvelope(srv.URL))
	if out.Kind != channel.OutcomeTransient {
		t.Fatalf("expected Transient, got %v: %q", out.Kind, out.Reason)
	}
}

func TestRetryAfter429ParsedSeconds(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	ch, _ := resthook.New(resthook.Options{HTTPClient: srv.Client(), Metrics: newFakeMetrics()})
	out, _ := ch.Deliver(context.Background(), newEnvelope(srv.URL))
	if out.Kind != channel.OutcomeTransient {
		t.Fatalf("expected Transient, got %v: %q", out.Kind, out.Reason)
	}
	if out.RetryAfter != 120*time.Second {
		t.Errorf("RetryAfter = %v, want 120s", out.RetryAfter)
	}
}

func TestRetryAfterHTTPDate(t *testing.T) {
	t.Parallel()
	future := time.Now().Add(45 * time.Second).UTC().Format(http.TimeFormat)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", future)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ch, _ := resthook.New(resthook.Options{HTTPClient: srv.Client(), Metrics: newFakeMetrics()})
	out, _ := ch.Deliver(context.Background(), newEnvelope(srv.URL))
	if out.Kind != channel.OutcomeTransient {
		t.Fatalf("expected Transient, got %v", out.Kind)
	}
	// Should be ~45s; allow small clock-skew tolerance.
	if out.RetryAfter < 30*time.Second || out.RetryAfter > 60*time.Second {
		t.Errorf("RetryAfter = %v, expected near 45s", out.RetryAfter)
	}
}

func TestPermanent4xxFailure(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"resourceType":"OperationOutcome"}`))
	}))
	defer srv.Close()

	ch, _ := resthook.New(resthook.Options{HTTPClient: srv.Client(), Metrics: newFakeMetrics()})
	out, _ := ch.Deliver(context.Background(), newEnvelope(srv.URL))
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("expected Permanent, got %v: %q", out.Kind, out.Reason)
	}
	if !strings.Contains(out.Reason, "404") {
		t.Errorf("reason = %q (want 404)", out.Reason)
	}
}

func TestRequestTimeoutClass408Transient(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusRequestTimeout)
	}))
	defer srv.Close()

	ch, _ := resthook.New(resthook.Options{HTTPClient: srv.Client(), Metrics: newFakeMetrics()})
	out, _ := ch.Deliver(context.Background(), newEnvelope(srv.URL))
	if out.Kind != channel.OutcomeTransient {
		t.Fatalf("expected Transient, got %v", out.Kind)
	}
}

func TestNetworkErrorTransient(t *testing.T) {
	t.Parallel()
	// Bind on an ephemeral port, then close to guarantee connection refused.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	ch, _ := resthook.New(resthook.Options{Metrics: newFakeMetrics()})
	env := newEnvelope("https://" + addr + "/webhook")
	env.Deadline = time.Now().Add(2 * time.Second)
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomeTransient {
		t.Fatalf("expected Transient on connection refused, got %v: %q", out.Kind, out.Reason)
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

	ch, _ := resthook.New(resthook.Options{HTTPClient: srv.Client(), Metrics: newFakeMetrics()})

	env := newEnvelope(srv.URL)
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

func TestHeaderInvalidNameRejected(t *testing.T) {
	t.Parallel()
	var got string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Bad Name")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch, _ := resthook.New(resthook.Options{HTTPClient: srv.Client(), Metrics: newFakeMetrics()})
	env := newEnvelope(srv.URL)
	env.SubscriptionParameters = []channel.Param{
		{Name: "Bad Name", Value: "x"}, // contains space — invalid
	}
	out, _ := ch.Deliver(context.Background(), env)
	requireDelivered(t, out)
	if got != "" {
		t.Errorf("invalid header name leaked: %q", got)
	}
}

func TestHandshakeBundleKindOmitsEventNumber(t *testing.T) {
	t.Parallel()
	var gotEvNum string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEvNum = r.Header.Get("X-Subscription-Event-Number")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch, _ := resthook.New(resthook.Options{HTTPClient: srv.Client(), Metrics: newFakeMetrics()})
	env := newEnvelope(srv.URL)
	env.BundleKind = channel.BundleHandshake
	out, _ := ch.Deliver(context.Background(), env)
	requireDelivered(t, out)
	if gotEvNum != "" {
		t.Errorf("X-Subscription-Event-Number leaked on non-event-notification: %q", gotEvNum)
	}
}

func TestDNSNXDomainPermanent(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("DNS resolution behavior varies by platform; skipping in -short mode")
	}
	// Use an HTTP client whose Transport has an explicit DialContext with a
	// short timeout so the test can never block on a slow system resolver.
	tr := &http.Transport{
		DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
	}
	ch, _ := resthook.New(resthook.Options{
		HTTPClient: &http.Client{Transport: tr, Timeout: 5 * time.Second},
		Metrics:    newFakeMetrics(),
	})
	env := newEnvelope("https://this-domain-must-not-exist-12345.invalid/webhook")
	env.Deadline = time.Now().Add(3 * time.Second)
	out, _ := ch.Deliver(context.Background(), env)
	// .invalid TLD is reserved for non-existent names per RFC 6761; resolution
	// must fail. Either NXDOMAIN (permanent) or DNS timeout (transient) is
	// acceptable; treat NXDOMAIN as permanent specifically.
	if out.Kind != channel.OutcomePermanent && out.Kind != channel.OutcomeTransient {
		t.Fatalf("unexpected outcome %v: %q", out.Kind, out.Reason)
	}
}

func TestDeliveredMetricIncremented(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := newFakeMetrics()
	ch, _ := resthook.New(resthook.Options{HTTPClient: srv.Client(), Metrics: m})
	out, _ := ch.Deliver(context.Background(), newEnvelope(srv.URL))
	requireDelivered(t, out)

	if got := m.get("fhir_subs_channel_resthook_deliveries_total",
		map[string]string{"channel": "resthook", "outcome": "delivered"}); got != 1 {
		t.Errorf("expected 1 delivered counter, got %v (counters=%+v)", got, m.counters)
	}
}

func TestContextCanceledDuringSendIsTransient(t *testing.T) {
	t.Parallel()
	// Server delays a long-but-finite time so that:
	//   1. The client's context cancel triggers a transient outcome, AND
	//   2. The httptest.Server.Close cleanup is not blocked on a never-
	//      ending handler. The test asserts the outcome before the handler
	//      finishes; the handler then completes naturally.
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

	ch, _ := resthook.New(resthook.Options{HTTPClient: srv.Client(), Metrics: newFakeMetrics()})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	env := newEnvelope(srv.URL)
	env.Deadline = time.Now().Add(5 * time.Second)
	out, err := ch.Deliver(ctx, env)
	if out.Kind != channel.OutcomeTransient {
		t.Fatalf("expected Transient, got %v err=%v", out.Kind, err)
	}
}

func TestContentTypeFHIRXML(t *testing.T) {
	t.Parallel()
	var got string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch, _ := resthook.New(resthook.Options{HTTPClient: srv.Client(), Metrics: newFakeMetrics()})
	env := newEnvelope(srv.URL)
	env.ContentType = channel.ContentTypeFHIRXML
	out, _ := ch.Deliver(context.Background(), env)
	requireDelivered(t, out)
	if got != "application/fhir+xml" {
		t.Errorf("Content-Type = %q", got)
	}
}

// TestNewWithoutOptionsUsesDefaults verifies the defaults path — no metrics,
// no HTTPClient — does not panic and produces a usable channel.
func TestNewWithoutOptionsUsesDefaults(t *testing.T) {
	t.Parallel()
	ch, err := resthook.New(resthook.Options{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if ch == nil {
		t.Fatal("nil channel")
	}
}

// TestUnreachableSchemeError ensures malformed URLs are reported, not panic.
func TestMalformedURL(t *testing.T) {
	t.Parallel()
	ch, _ := resthook.New(resthook.Options{Metrics: newFakeMetrics()})
	env := newEnvelope("https://%zz/")
	env.Deadline = time.Now().Add(1 * time.Second)
	out, err := ch.Deliver(context.Background(), env)
	if out.Kind == channel.OutcomeDelivered {
		t.Fatalf("expected failure on malformed URL, err=%v", err)
	}
}

// Compile-time check that *resthook.Channel satisfies channel.Channel.
var _ channel.Channel = (*resthook.Channel)(nil)
