// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package resthook_test

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/resthook"
)

// S-4 (default http.Client no Timeout): the channel's default-constructed
// HTTP client must carry a Timeout that bounds the entire request, so a
// hostile subscriber that drips response headers cannot tie up the worker
// past its envelope deadline.
func TestRestHook_DefaultClientHasTimeout(t *testing.T) {
	t.Parallel()
	ch, err := resthook.New(resthook.Options{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	httpClient := ch.HTTPClientForTest()
	if httpClient.Timeout <= 0 {
		t.Fatalf("default client Timeout = %v; want > 0", httpClient.Timeout)
	}
	if httpClient.Timeout > 5*time.Minute {
		t.Fatalf("default client Timeout = %v; want a sane bound", httpClient.Timeout)
	}
}

// S-4 (TLS / pool knobs): operators must be able to override
// MaxIdleConnsPerHost, MaxConnsPerHost, and TLS min-version. We expose
// them via Options so the channel doesn't need to wrap an entire
// http.Client to tune the pool.
func TestRestHook_TLSAndPoolKnobs(t *testing.T) {
	t.Parallel()
	ch, err := resthook.New(resthook.Options{
		MaxIdleConnsPerHost: 5,
		MaxConnsPerHost:     8,
		TLSMinVersion:       tls.VersionTLS13,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	tr := ch.TransportForTest()
	if tr == nil {
		t.Fatalf("transport is nil")
	}
	if tr.MaxIdleConnsPerHost != 5 {
		t.Errorf("MaxIdleConnsPerHost = %d; want 5", tr.MaxIdleConnsPerHost)
	}
	if tr.MaxConnsPerHost != 8 {
		t.Errorf("MaxConnsPerHost = %d; want 8", tr.MaxConnsPerHost)
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.MinVersion != tls.VersionTLS13 {
		t.Errorf("TLSClientConfig.MinVersion not set to TLS 1.3")
	}
}

// S-4 (no max bundle size): payload=full-resource with embedded base64
// can produce multi-MB bundles. The channel must refuse to deliver a
// bundle larger than MaxBundleBytes with a permanent failure rather
// than POST it (and burn retries on it).
func TestRestHook_RejectsOversizedBundle(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	ch, err := resthook.New(resthook.Options{
		HTTPClient:     srv.Client(),
		MaxBundleBytes: 256,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	env := newEnvelope(srv.URL + "/webhook")
	env.BundleBytes = make([]byte, 1024) // 1 KiB > 256 cap
	for i := range env.BundleBytes {
		env.BundleBytes[i] = 'x'
	}
	out, err := ch.Deliver(context.Background(), env)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("kind = %v; want Permanent", out.Kind)
	}
	if !strings.Contains(out.Reason, "bundle too large") {
		t.Fatalf("reason = %q; want bundle too large", out.Reason)
	}
}

// S-4 (allowSubscriberHeader default-permit): subscribers must NOT be
// able to forge headers like X-Internal-Trust or X-Auth-User just
// because they pass the validity regex. Subscriber-supplied headers
// default-deny — only the allowedFHIRHeaders set + an explicit
// per-subscription allowlist let a header through.
func TestRestHook_RejectsUnknownSubscriberHeaders(t *testing.T) {
	t.Parallel()
	var gotInternal, gotAuth, gotPrefer string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotInternal = r.Header.Get("X-Internal-Trust")
		gotAuth = r.Header.Get("X-Auth-User")
		gotPrefer = r.Header.Get("Prefer")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	ch, err := resthook.New(resthook.Options{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	env := newEnvelope(srv.URL + "/webhook")
	env.SubscriptionParameters = []channel.Param{
		{Name: "X-Internal-Trust", Value: "yes"},  // not in allowlist → must be filtered
		{Name: "X-Auth-User", Value: "root"},      // not in allowlist → must be filtered
		{Name: "Prefer", Value: "return=minimal"}, // explicit allowedFHIRHeaders → allowed
	}
	out, err := ch.Deliver(context.Background(), env)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if out.Kind != channel.OutcomeDelivered {
		t.Fatalf("kind=%v; want Delivered", out.Kind)
	}
	if gotInternal != "" {
		t.Errorf("X-Internal-Trust forwarded with value %q; default-permit gap", gotInternal)
	}
	if gotAuth != "" {
		t.Errorf("X-Auth-User forwarded with value %q; default-permit gap", gotAuth)
	}
	if gotPrefer != "return=minimal" {
		t.Errorf("Prefer = %q; want return=minimal (allowed FHIR header)", gotPrefer)
	}
}

// S-4 (NXDOMAIN classified Permanent): NXDOMAIN at the resolver level
// is frequently a transient propagation issue (SOA refresh in flight,
// recursive resolver hiccup). Treat it as Transient so the scheduler
// can retry rather than dead-letter immediately.
func TestRestHook_NXDomainClassifiedTransient(t *testing.T) {
	t.Parallel()
	// Use an .invalid TLD per RFC 2606 — guaranteed NXDOMAIN.
	ch, err := resthook.New(resthook.Options{HTTPClient: &http.Client{Timeout: 2 * time.Second}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	env := newEnvelope("https://nonexistent-host-for-test.invalid/x")
	out, err := ch.Deliver(context.Background(), env)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if out.Kind != channel.OutcomeTransient {
		t.Fatalf("kind=%v reason=%q; want Transient", out.Kind, out.Reason)
	}
}

// S-4 (readBodyExcerpt PHI leak): the channel must not surface
// subscriber 4xx response body text into out.Reason when the operator
// has disabled body excerpts (the default for production). Set
// IncludeResponseBodyExcerpt=false: only "<status> <text>" appears.
func TestRestHook_BodyExcerptOptOut(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Patient JOHN DOE MRN 12345 already exists"))
	}))
	defer srv.Close()

	ch, err := resthook.New(resthook.Options{
		HTTPClient:                 srv.Client(),
		IncludeResponseBodyExcerpt: false,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	env := newEnvelope(srv.URL + "/webhook")
	out, err := ch.Deliver(context.Background(), env)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("kind=%v; want Permanent", out.Kind)
	}
	if strings.Contains(out.Reason, "JOHN DOE") || strings.Contains(out.Reason, "MRN") {
		t.Fatalf("reason leaks subscriber body bytes (potential PHI): %q", out.Reason)
	}
	if !strings.Contains(out.Reason, "400") {
		t.Fatalf("reason = %q; should still carry status", out.Reason)
	}
}
