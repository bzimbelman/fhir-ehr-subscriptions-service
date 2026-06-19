// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	hpipe "github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/harness"
)

// TestE2E_S12_9_RejectFHIRXMLOnCreate exercises the runtime API:
// POST /Subscription with `application/fhir+xml` (in either the R5
// `contentType` field or the R4B `channel.payload` field) must return
// 400 + an OperationOutcome that mentions `fhir+xml`.
func TestE2E_S12_9_RejectFHIRXMLOnCreate(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resetPipelineTables(t, ctx, h)

	clientID := "client-s12_9-create-" + uuid.New().String()[:8]
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

	cases := []struct {
		name string
		body map[string]any
	}{
		{
			name: "r5_contentType",
			body: map[string]any{
				"resourceType": "Subscription",
				"status":       "requested",
				"topic":        "http://example.org/topics/hl7-passthrough",
				"channelType":  map[string]any{"code": "rest-hook"},
				"endpoint":     "https://example.org/wh",
				"content":      "id-only",
				"contentType":  "application/fhir+xml",
				"channel": map[string]any{
					"type":     "rest-hook",
					"endpoint": "https://example.org/wh",
				},
			},
		},
		{
			name: "r4b_channel_payload",
			body: map[string]any{
				"resourceType": "Subscription",
				"status":       "requested",
				"topic":        "http://example.org/topics/hl7-passthrough",
				"channelType":  map[string]any{"code": "rest-hook"},
				"endpoint":     "https://example.org/wh",
				"content":      "id-only",
				"channel": map[string]any{
					"type":     "rest-hook",
					"endpoint": "https://example.org/wh",
					"payload":  "application/fhir+xml",
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.body)
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, api.URL+"/Subscription", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/fhir+json")
			resp, err := api.Client().Do(req)
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
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
		})
	}
}

// TestE2E_S12_9_RejectFHIRXMLOnUpdate exercises the runtime API:
// PUT /Subscription/{id} with `channel.payload=application/fhir+xml`
// must also return 400 + OperationOutcome. The store row must NOT be
// mutated by the rejected request.
func TestE2E_S12_9_RejectFHIRXMLOnUpdate(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resetPipelineTables(t, ctx, h)

	clientID := "client-s12_9-update-" + uuid.New().String()[:8]
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

	createBody, _ := json.Marshal(map[string]any{
		"resourceType": "Subscription",
		"status":       "requested",
		"topic":        "http://example.org/topics/hl7-passthrough",
		"channelType":  map[string]any{"code": "rest-hook"},
		"endpoint":     "https://example.org/wh",
		"content":      "id-only",
		"channel": map[string]any{
			"type":     "rest-hook",
			"endpoint": "https://example.org/wh",
		},
	})
	id, err := hpipe.PostSubscription(ctx, api, api.Client(), createBody)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	updateBody, _ := json.Marshal(map[string]any{
		"resourceType": "Subscription",
		"status":       "active",
		"topic":        "http://example.org/topics/hl7-passthrough",
		"channelType":  map[string]any{"code": "rest-hook"},
		"endpoint":     "https://example.org/wh-new",
		"content":      "id-only",
		"channel": map[string]any{
			"type":    "rest-hook",
			"payload": "application/fhir+xml",
		},
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, api.URL+"/Subscription/"+id.String(), bytes.NewReader(updateBody))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := api.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
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

	// Read back: endpoint must NOT have changed to wh-new.
	getReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, api.URL+"/Subscription/"+id.String(), nil)
	getResp, err := api.Client().Do(getReq)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = getResp.Body.Close() }()
	getBody, _ := io.ReadAll(getResp.Body)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", getResp.StatusCode, getBody)
	}
	var got map[string]any
	_ = json.Unmarshal(getBody, &got)
	if ep, _ := got["endpoint"].(string); ep != "https://example.org/wh" {
		t.Errorf("endpoint mutated despite rejection: %q", ep)
	}
}
