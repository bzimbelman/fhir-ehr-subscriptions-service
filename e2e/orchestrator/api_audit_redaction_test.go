// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	hpipe "github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/harness"
)

// TestAPIAuditRedaction posts a Subscription with a bearer token in
// channel.header[]; the audit_log row's canonical_form must NOT
// contain the secret. (B-13)
func TestAPIAuditRedaction(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resetPipelineTables(t, ctx, h)

	clientID := "client-audit-" + uuid.New().String()[:8]
	api, err := hpipe.StartAPIServer(ctx, hpipe.APIServerConfig{
		Pool:     h.DB,
		ClientID: clientID,
	})
	if err != nil {
		t.Fatalf("api start: %v", err)
	}
	t.Cleanup(func() { _ = api.Close() })

	if err := seedHL7Topic(ctx, h.DB); err != nil {
		t.Fatalf("seed topic: %v", err)
	}

	const secret = "audit-redaction-secret-token-1234567890"
	body, _ := json.Marshal(map[string]any{
		"resourceType": "Subscription",
		"status":       "requested",
		"topic":        "http://example.org/topics/hl7-passthrough",
		"channelType":  map[string]any{"code": "rest-hook"},
		"endpoint":     "https://example.org/wh",
		"content":      "id-only",
		"channel": map[string]any{
			"type":     "rest-hook",
			"endpoint": "https://example.org/wh",
			"header":   []string{"Authorization: Bearer " + secret},
		},
	})

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, api.URL+"/Subscription/", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := api.Client().Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != 201 {
		t.Fatalf("status=%d, want 201", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	_ = resp.Body.Close()
	subID, err := uuid.Parse(strings.TrimPrefix(loc, "/Subscription/"))
	if err != nil {
		t.Fatalf("parse id: %v", err)
	}

	// audit_log is appended async-friendly inside the create handler;
	// the row is committed before the 201 is sent so it should be
	// visible immediately. Be tolerant of one quick retry.
	var rows []string
	for i := 0; i < 3; i++ {
		rows, err = getAuditCanonicals(ctx, h.DB, subID)
		if err != nil {
			t.Fatalf("query audit_log: %v", err)
		}
		if len(rows) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(rows) == 0 {
		t.Fatalf("expected at least one audit_log row for subscription %s", subID)
	}
	for i, body := range rows {
		if strings.Contains(body, secret) {
			t.Fatalf("audit_log row %d leaked secret token: %s", i, body)
		}
	}
	// At least one row should carry the redaction marker, so we know
	// the redactor ran (rather than the body simply being absent).
	hasRedacted := false
	for _, body := range rows {
		if strings.Contains(body, "[REDACTED]") {
			hasRedacted = true
			break
		}
	}
	if !hasRedacted {
		t.Fatalf("audit_log rows did not carry [REDACTED] placeholder: %v", rows)
	}
}
