// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	hpipe "github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/harness"
)

// TestE2E_S2_EventsRejectsInvalidSinceParam exercises the runtime
// API: a malformed `eventsSinceNumber` must produce 400 OperationOutcome
// rather than silently treating it as 0. (S-2.10)
func TestE2E_S2_EventsRejectsInvalidSinceParam(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resetPipelineTables(t, ctx, h)

	clientID := "client-s2-events-" + uuid.New().String()[:8]
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

	// Create a subscription so $events has a target id.
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
		},
	})
	id, err := hpipe.PostSubscription(ctx, api, api.Client(), body)
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	cases := []string{
		"eventsSinceNumber=abc",
		"eventsSinceNumber=-1",
		"eventsUntilNumber=foo",
	}
	for _, q := range cases {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, api.URL+"/Subscription/"+id.String()+"/$events?"+q, nil)
		resp, err := api.Client().Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", q, err)
		}
		bb, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("$events?%s status = %d; want 400; body=%s", q, resp.StatusCode, bb)
		}
	}
}

// TestE2E_S2_StatusBulkCapped exercises the runtime API: more than the
// configured cap of `id` query params must produce 400 rather than
// fanning out a bunch of GetByID calls. (S-2.11)
func TestE2E_S2_StatusBulkCapped(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resetPipelineTables(t, ctx, h)

	clientID := "client-s2-bulk-" + uuid.New().String()[:8]
	api, err := hpipe.StartAPIServer(ctx, hpipe.APIServerConfig{
		Pool:     h.DB,
		ClientID: clientID,
	})
	if err != nil {
		t.Fatalf("api start: %v", err)
	}
	t.Cleanup(func() { _ = api.Close() })

	parts := make([]string, 0, 257)
	for i := 0; i < 257; i++ {
		parts = append(parts, "id="+fmt.Sprintf("%d-0000-0000-0000-000000000000", i+1000))
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, api.URL+"/Subscription/$status?"+strings.Join(parts, "&"), nil)
	resp, err := api.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		bb, _ := io.ReadAll(resp.Body)
		t.Fatalf("$status status = %d; want 400; body=%s", resp.StatusCode, bb)
	}
}

// TestE2E_S2_MetadataInstantUsesZForm exercises the runtime API:
// /metadata.date (a FHIR `instant`) must use the trailing `Z` form,
// not `+00:00`. (S-2.9)
func TestE2E_S2_MetadataInstantUsesZForm(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resetPipelineTables(t, ctx, h)

	clientID := "client-s2-meta-" + uuid.New().String()[:8]
	api, err := hpipe.StartAPIServer(ctx, hpipe.APIServerConfig{
		Pool:     h.DB,
		ClientID: clientID,
	})
	if err != nil {
		t.Fatalf("api start: %v", err)
	}
	t.Cleanup(func() { _ = api.Close() })

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, api.URL+"/metadata", nil)
	resp, err := api.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /metadata: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	bb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metadata status = %d; want 200; body=%s", resp.StatusCode, bb)
	}
	if strings.Contains(string(bb), "+00:00") {
		t.Fatalf("/metadata.date must use Z form, not +00:00: %s", bb)
	}
	var cs map[string]any
	if err := json.Unmarshal(bb, &cs); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, bb)
	}
	if cs["fhirVersion"] == nil || cs["fhirVersion"] == "" {
		t.Fatalf("fhirVersion missing: %+v", cs)
	}
}

// TestE2E_S2_BodySizeOversize413 exercises the runtime API: a body
// well over the default 1 MiB cap must be rejected with 413 rather
// than silently truncated. (S-2.2)
func TestE2E_S2_BodySizeOversize413(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resetPipelineTables(t, ctx, h)

	clientID := "client-s2-body-" + uuid.New().String()[:8]
	api, err := hpipe.StartAPIServer(ctx, hpipe.APIServerConfig{
		Pool:     h.DB,
		ClientID: clientID,
	})
	if err != nil {
		t.Fatalf("api start: %v", err)
	}
	t.Cleanup(func() { _ = api.Close() })

	// Default cap is 1 MiB; send 2 MiB.
	huge := strings.Repeat("a", 2<<20)
	body := `{"resourceType":"Subscription","junk":"` + huge + `"}`
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, api.URL+"/Subscription/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := api.Client().Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		bb, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d; want 413; body excerpt=%s", resp.StatusCode, bb[:min(200, len(bb))])
	}
}
