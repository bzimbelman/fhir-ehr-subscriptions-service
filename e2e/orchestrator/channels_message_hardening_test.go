// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/message"
)

// validInnerBundleE2E builds a subscription-notification Bundle the
// message channel can wrap. Mirrors the unit-test helper.
func validInnerBundleE2E(t *testing.T, statusType string) []byte {
	t.Helper()
	inner := map[string]any{
		"resourceType": "Bundle",
		"id":           uuid.NewString(),
		"type":         "history",
		"entry": []any{
			map[string]any{
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

// TestE2E_Message_TimestampIsRFC3339Nano exercises the S-5 fix: the
// outer Bundle.timestamp serializes with sub-second precision (FHIR
// `instant`), not whole-second RFC3339.
func TestE2E_Message_TimestampIsRFC3339Nano(t *testing.T) {
	t.Parallel()
	var captured []byte
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch, err := message.New(message.Options{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	env := channel.NotificationEnvelope{
		SubscriptionID:       uuid.New(),
		Sequence:             1,
		BundleBytes:          validInnerBundleE2E(t, "event-notification"),
		BundleKind:           channel.BundleEventNotification,
		PayloadType:          channel.PayloadIDOnly,
		ContentType:          channel.ContentTypeFHIRJSON,
		Attempt:              1,
		CorrelationID:        uuid.New().String(),
		SubscriptionEndpoint: srv.URL + "/msg",
		Deadline:             time.Now().Add(10 * time.Second),
	}
	out, err := ch.Deliver(context.Background(), env)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if out.Kind != channel.OutcomeDelivered {
		t.Fatalf("kind=%v reason=%q", out.Kind, out.Reason)
	}

	var outer struct {
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(captured, &outer); err != nil {
		t.Fatalf("unmarshal outer: %v body=%s", err, string(captured))
	}
	if _, err := time.Parse(time.RFC3339Nano, outer.Timestamp); err != nil {
		t.Fatalf("timestamp %q does not parse as RFC3339Nano: %v", outer.Timestamp, err)
	}
	if !strings.Contains(outer.Timestamp, ".") {
		t.Fatalf("timestamp %q lacks fractional seconds (FHIR `instant`)", outer.Timestamp)
	}
}

// TestE2E_Message_ValidateContentTypeAtBoundary exercises the S-5 fix:
// the channel exposes a ValidateContentType primitive callers can use
// to reject non-fhir+json subscriptions at create time.
func TestE2E_Message_ValidateContentTypeAtBoundary(t *testing.T) {
	t.Parallel()
	ch, err := message.New(message.Options{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := ch.ValidateContentType(channel.ContentTypeFHIRJSON); err != nil {
		t.Errorf("fhir+json should be accepted: %v", err)
	}
	if err := ch.ValidateContentType(channel.ContentTypeFHIRXML); err == nil {
		t.Errorf("fhir+xml should be rejected at create time")
	}
}
