// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Tests covering S-12.9 in the production-readiness audit: the API path
// (POST + PUT /Subscription) must reject `application/fhir+xml` content
// types up front so the client gets immediate feedback. The builder's
// runtime guard (internal/engine/builder/builder.go:36-38) is intended
// as defense-in-depth; the API rejection is the authoritative check.
//
// "fhir+xml" can arrive on the body in either of two places:
//   - R5 native:        top-level `contentType` field
//   - R4B Backport:     `channel.payload` field (carries the content type
//     in the legacy shape)
//
// Both shapes must be rejected with HTTP 400 + an OperationOutcome whose
// diagnostics text mentions `fhir+xml` so the client knows what to fix.
package handlers_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// TestCreateSubscription_RejectsFHIRXML_R5 — POST with top-level
// contentType=application/fhir+xml must 400.
func TestCreateSubscription_RejectsFHIRXML_R5(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	srv := newTestServer(t, defaultPrincipal(), deps)

	body := `{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.org/webhook",
		"content": "id-only",
		"contentType": "application/fhir+xml",
		"channel": {"type": "rest-hook", "endpoint": "https://example.org/webhook"}
	}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/Subscription", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400; body=%s", resp.StatusCode, respBody)
	}
	if !strings.Contains(string(respBody), "OperationOutcome") {
		t.Errorf("expected OperationOutcome envelope; got %s", respBody)
	}
	if !strings.Contains(string(respBody), "fhir+xml") {
		t.Errorf("expected diagnostics to mention fhir+xml; got %s", respBody)
	}
}

// TestCreateSubscription_RejectsFHIRXML_R4BPayload — POST with R4B
// Backport `channel.payload=application/fhir+xml` must 400 too. The
// payload field is treated as the content type when it isn't one of
// the {empty, id-only, full-resource} content codes.
func TestCreateSubscription_RejectsFHIRXML_R4BPayload(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	srv := newTestServer(t, defaultPrincipal(), deps)

	body := `{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.org/webhook",
		"content": "id-only",
		"channel": {
			"type": "rest-hook",
			"endpoint": "https://example.org/webhook",
			"payload": "application/fhir+xml"
		}
	}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/Subscription", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400; body=%s", resp.StatusCode, respBody)
	}
	if !strings.Contains(string(respBody), "OperationOutcome") {
		t.Errorf("expected OperationOutcome envelope; got %s", respBody)
	}
	if !strings.Contains(string(respBody), "fhir+xml") {
		t.Errorf("expected diagnostics to mention fhir+xml; got %s", respBody)
	}
}

// TestUpdateSubscription_RejectsFHIRXML — PUT with fhir+xml in
// channel.payload also rejects, so a client cannot sneak the bad
// content type in via an update.
func TestUpdateSubscription_RejectsFHIRXML(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	id, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "rest-hook",
		Endpoint:    "https://example.org/wh",
		Content:     "id-only",
		MaxCount:    1,
	})
	srv := newTestServer(t, defaultPrincipal(), deps)
	body := `{
		"resourceType": "Subscription",
		"status": "active",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.org/wh",
		"content": "id-only",
		"channel": {"type": "rest-hook", "payload": "application/fhir+xml"}
	}`
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/Subscription/"+id.String(), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400; body=%s", resp.StatusCode, respBody)
	}
	if !strings.Contains(string(respBody), "OperationOutcome") {
		t.Errorf("expected OperationOutcome envelope; got %s", respBody)
	}
	if !strings.Contains(string(respBody), "fhir+xml") {
		t.Errorf("expected diagnostics to mention fhir+xml; got %s", respBody)
	}
	// The store must NOT have been mutated.
	row, _ := subs.GetByID(context.Background(), id)
	if row == nil {
		t.Fatalf("subscription disappeared")
	}
	if row.Endpoint != "https://example.org/wh" {
		t.Errorf("endpoint changed despite rejection: %s", row.Endpoint)
	}
}

// TestCreateSubscription_FHIRJSON_HappyPath_Unchanged — the JSON happy
// path keeps working; this is the regression guard for the rejection
// logic so it doesn't accidentally reject fhir+json.
func TestCreateSubscription_FHIRJSON_HappyPath_Unchanged(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	srv := newTestServer(t, defaultPrincipal(), deps)

	body := `{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.org/webhook",
		"content": "id-only",
		"contentType": "application/fhir+json",
		"channel": {"type": "rest-hook", "endpoint": "https://example.org/webhook", "payload": "id-only"}
	}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/Subscription", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d; want 201; body=%s", resp.StatusCode, respBody)
	}
}
