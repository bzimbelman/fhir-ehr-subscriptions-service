// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package resthook_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/resthook"
)

// flippingResolver returns the first IP set on the first LookupIP call
// and the second IP set on every subsequent call. It models a hostile
// subscriber that registers a public-resolving hostname at create time
// and flips DNS to a private/CG-NAT/metadata IP before delivery (DNS
// rebinding, audit finding #112 sup2 / #129 / OP #182).
type flippingResolver struct {
	calls   atomic.Int64
	first   []net.IP
	rest    []net.IP
	restErr error
}

func (r *flippingResolver) LookupIP(_ context.Context, _, _ string) ([]net.IP, error) {
	n := r.calls.Add(1)
	if n == 1 {
		return r.first, nil
	}
	if r.restErr != nil {
		return nil, r.restErr
	}
	return r.rest, nil
}

// TestDeliver_RevalidatesURLAtDeliveryTime asserts that a hostname
// which resolved to a public IP at create time but flips to an RFC1918
// private IP before Deliver is rejected by the channel without dialing.
//
// The validator is the production handlers.URLValidator built with a
// resolver whose second-call answer is private. The channel is
// constructed with this validator wired in; we pre-arm the validator's
// resolver so the first call (create-time, simulated by calling
// Validate explicitly here) returns public, and Deliver triggers the
// second call which must reject.
//
// No mocks: real handlers.URLValidator, real net.IP, real *http.Client.
// The custom resolver implements the production handlers.Resolver
// interface; that interface exists in production for DI and is the
// same seam URLValidator uses against net.DefaultResolver in real
// deployments.
func TestDeliver_RevalidatesURLAtDeliveryTime(t *testing.T) {
	t.Parallel()

	resolver := &flippingResolver{
		first: []net.IP{net.ParseIP("93.184.216.34")}, // example.com (public)
		rest:  []net.IP{net.ParseIP("10.0.0.5")},      // RFC1918 private after flip
	}
	validator := handlers.NewURLValidator(handlers.URLValidatorConfig{
		Resolver: resolver,
	})

	// Create-time validation: simulate the API path that successfully
	// validates the endpoint when the subscription is first POSTed.
	if err := validator.Validate(context.Background(), "https://victim.example.com/notify"); err != nil {
		t.Fatalf("create-time Validate must pass: %v", err)
	}

	// A live receiver — if the channel mistakenly proceeds to dial,
	// it would actually send the bundle. We assert the receiver was
	// NOT called.
	var receiverHit atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		receiverHit.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	ch, err := resthook.New(resthook.Options{
		HTTPClient:   srv.Client(),
		Metrics:      newFakeMetrics(),
		URLValidator: validator,
	})
	if err != nil {
		t.Fatalf("resthook.New: %v", err)
	}
	t.Cleanup(func() { _ = ch.Close() })

	out, err := ch.Deliver(context.Background(), newEnvelope("https://victim.example.com/notify"))
	if err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("expected OutcomePermanent (SSRF rejection), got %v reason=%q", out.Kind, out.Reason)
	}
	if !strings.Contains(strings.ToLower(out.Reason), "ssrf") &&
		!strings.Contains(strings.ToLower(out.Reason), "blocked") &&
		!strings.Contains(strings.ToLower(out.Reason), "private") &&
		!strings.Contains(strings.ToLower(out.Reason), "policy") {
		t.Fatalf("expected SSRF-shaped reason, got %q", out.Reason)
	}
	if receiverHit.Load() {
		t.Fatalf("delivery dialed the subscriber after DNS rebound to private; SSRF policy bypassed")
	}
	// Validator must have been called twice: once at create, once at
	// delivery. If the channel skipped the re-check, calls would be 1.
	if got := resolver.calls.Load(); got < 2 {
		t.Fatalf("expected validator to re-resolve at delivery time (>=2 calls), got %d", got)
	}
}

// TestDeliver_HTTPSchemeWithValidatorOptIn asserts that an http://
// endpoint delivers when the operator opts in via a URLValidator built
// with AllowHTTP=true and an AllowHosts entry for the subscriber. This
// is the demo / dev path: the bridge accepts http:// at create-time and
// at delivery-time the channel must defer scheme allowance to the same
// validator instance, not re-impose a hardcoded https-only check on top.
// OP #286 — the demo walkthrough fanned out events into deliveries,
// then dead-lettered every one because the channel hard-required https
// regardless of validator opt-in.
func TestDeliver_HTTPSchemeWithValidatorOptIn(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	// Use the test server's host (a 127.0.0.1 loopback) and add it to
	// AllowHosts so the SSRF policy lets it through. Without that entry
	// the validator would block loopback at delivery time even with
	// AllowHTTP=true.
	srvURL := strings.TrimPrefix(srv.URL, "http://")
	host := srvURL
	if i := strings.IndexByte(srvURL, ':'); i >= 0 {
		host = srvURL[:i]
	}
	validator := handlers.NewURLValidator(handlers.URLValidatorConfig{
		AllowHTTP:  true,
		AllowHosts: []string{host},
	})

	ch, err := resthook.New(resthook.Options{
		HTTPClient:   srv.Client(),
		Metrics:      newFakeMetrics(),
		URLValidator: validator,
	})
	if err != nil {
		t.Fatalf("resthook.New: %v", err)
	}
	t.Cleanup(func() { _ = ch.Close() })

	out, err := ch.Deliver(context.Background(), newEnvelope(srv.URL))
	if err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}
	if out.Kind != channel.OutcomeDelivered {
		t.Fatalf("expected OutcomeDelivered with AllowHTTP+AllowHosts opt-in, got %v reason=%q", out.Kind, out.Reason)
	}
}

// TestDeliver_HTTPSchemeBlockedWhenNoValidator pins the safe default:
// when the channel is constructed without a URLValidator, an http://
// endpoint is rejected at delivery time. The hardcoded scheme check
// remains the only defense in this configuration. OP #286 regression
// guard.
func TestDeliver_HTTPSchemeBlockedWhenNoValidator(t *testing.T) {
	t.Parallel()

	ch, err := resthook.New(resthook.Options{
		Metrics: newFakeMetrics(),
		// URLValidator deliberately omitted.
	})
	if err != nil {
		t.Fatalf("resthook.New: %v", err)
	}
	t.Cleanup(func() { _ = ch.Close() })

	out, err := ch.Deliver(context.Background(), newEnvelope("http://insecure.example/webhook"))
	if err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("expected OutcomePermanent for http:// without validator, got %v", out.Kind)
	}
	if !strings.Contains(strings.ToLower(out.Reason), "non-https") {
		t.Fatalf("expected non-https reason, got %q", out.Reason)
	}
}

// TestDeliver_HTTPSchemeBlockedWhenValidatorAllowHTTPFalse pins that an
// operator who wires a URLValidator with AllowHTTP=false (the production
// default) still rejects http:// endpoints at delivery time. This is the
// production-deployment case. OP #286 regression guard.
func TestDeliver_HTTPSchemeBlockedWhenValidatorAllowHTTPFalse(t *testing.T) {
	t.Parallel()

	validator := handlers.NewURLValidator(handlers.URLValidatorConfig{
		AllowHTTP: false,
	})
	ch, err := resthook.New(resthook.Options{
		Metrics:      newFakeMetrics(),
		URLValidator: validator,
	})
	if err != nil {
		t.Fatalf("resthook.New: %v", err)
	}
	t.Cleanup(func() { _ = ch.Close() })

	out, err := ch.Deliver(context.Background(), newEnvelope("http://insecure.example/webhook"))
	if err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("expected OutcomePermanent for http:// with AllowHTTP=false validator, got %v", out.Kind)
	}
}

// TestDeliver_RevalidatesURLAtDeliveryTime_DNSError covers the case
// where the hostname stops resolving entirely between create and
// delivery (NXDOMAIN). The channel must reject without dialing.
func TestDeliver_RevalidatesURLAtDeliveryTime_DNSError(t *testing.T) {
	t.Parallel()

	resolver := &flippingResolver{
		first:   []net.IP{net.ParseIP("93.184.216.34")},
		restErr: errors.New("nxdomain"),
	}
	validator := handlers.NewURLValidator(handlers.URLValidatorConfig{
		Resolver: resolver,
	})
	if err := validator.Validate(context.Background(), "https://flipper.example.com/notify"); err != nil {
		t.Fatalf("create-time Validate must pass: %v", err)
	}

	var receiverHit atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		receiverHit.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	ch, err := resthook.New(resthook.Options{
		HTTPClient:   srv.Client(),
		Metrics:      newFakeMetrics(),
		URLValidator: validator,
	})
	if err != nil {
		t.Fatalf("resthook.New: %v", err)
	}
	t.Cleanup(func() { _ = ch.Close() })

	out, err := ch.Deliver(context.Background(), newEnvelope("https://flipper.example.com/notify"))
	if err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("expected OutcomePermanent on DNS failure at delivery, got %v reason=%q", out.Kind, out.Reason)
	}
	if receiverHit.Load() {
		t.Fatalf("delivery dialed despite DNS lookup failure at delivery time")
	}
}
