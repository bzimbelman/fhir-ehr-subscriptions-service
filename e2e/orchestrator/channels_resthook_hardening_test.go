// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/resthook"
)

// TestE2E_RestHook_DenyListedHeadersFiltered exercises the S-4 fix:
// subscriber-supplied headers that match the expanded deny list
// (X-Internal-*, X-Auth-*, X-Trusted-*) MUST be filtered. The receiver
// asserts none of the forged trust headers reach the wire.
func TestE2E_RestHook_DenyListedHeadersFiltered(t *testing.T) {
	t.Parallel()
	type seen struct {
		internalTrust string
		authUser      string
		trustedRoles  string
		realIP        string
		prefer        string
	}
	var got seen
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.internalTrust = r.Header.Get("X-Internal-Trust")
		got.authUser = r.Header.Get("X-Auth-User")
		got.trustedRoles = r.Header.Get("X-Trusted-Roles")
		got.realIP = r.Header.Get("X-Real-IP")
		got.prefer = r.Header.Get("Prefer")
		w.WriteHeader(http.StatusOK)
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
		SubscriptionParameters: []channel.Param{
			{Name: "X-Internal-Trust", Value: "yes"},
			{Name: "X-Auth-User", Value: "root"},
			{Name: "X-Trusted-Roles", Value: "admin"},
			{Name: "X-Real-IP", Value: "203.0.113.5"},
			{Name: "Prefer", Value: "return=minimal"}, // legitimate FHIR header
		},
	}
	out, err := ch.Deliver(context.Background(), env)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if out.Kind != channel.OutcomeDelivered {
		t.Fatalf("kind=%v reason=%q", out.Kind, out.Reason)
	}
	if got.internalTrust != "" || got.authUser != "" || got.trustedRoles != "" || got.realIP != "" {
		t.Fatalf("forged headers leaked: %+v", got)
	}
	if got.prefer != "return=minimal" {
		t.Errorf("Prefer = %q (legitimate FHIR header should pass)", got.prefer)
	}
}

// TestE2E_RestHook_BodyExcerptOptOut exercises the S-4 fix: subscriber
// 4xx response bodies are NOT quoted into out.Reason by default —
// IncludeResponseBodyExcerpt must be explicitly set true.
func TestE2E_RestHook_BodyExcerptOptOut(t *testing.T) {
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
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("kind=%v; want Permanent", out.Kind)
	}
	if strings.Contains(out.Reason, "JOHN DOE") || strings.Contains(out.Reason, "MRN") {
		t.Fatalf("body excerpt leaked into reason (potential PHI): %q", out.Reason)
	}
}

// TestE2E_RestHook_RejectsOversizedBundle exercises the S-4 fix: the
// channel refuses bundles larger than MaxBundleBytes BEFORE any I/O —
// the receiver MUST never see the request.
func TestE2E_RestHook_RejectsOversizedBundle(t *testing.T) {
	t.Parallel()
	var hit bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
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
	bundle := make([]byte, 1024)
	for i := range bundle {
		bundle[i] = 'x'
	}
	env := channel.NotificationEnvelope{
		SubscriptionID:       uuid.New(),
		Sequence:             1,
		BundleBytes:          bundle,
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
	if hit {
		t.Fatalf("receiver saw the request — channel should have refused before I/O")
	}
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("kind=%v reason=%q; want Permanent", out.Kind, out.Reason)
	}
	if !strings.Contains(out.Reason, "bundle too large") {
		t.Fatalf("reason=%q; want bundle-too-large", out.Reason)
	}
}
