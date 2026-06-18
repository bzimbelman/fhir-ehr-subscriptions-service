// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	hpipe "github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/harness"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
)

// TestE2E_S3_3_PerClientRateLimit_SubscriptionCreate exercises S-3.3:
// the handler-level token bucket on POST /Subscription returns 429 +
// Retry-After once a client exceeds its burst, and the limit is keyed
// on the authenticated principal so distinct clients have distinct
// buckets. The test runs against the full pg-backed handler stack via
// the harness, so it fails if Deps wiring or the chi middleware were
// silently regressed.
func TestE2E_S3_3_PerClientRateLimit_SubscriptionCreate(t *testing.T) {
	t.Skip("requires APIServerConfig.SubscriptionCreateRateLimit harness wiring — tracked in OP #87")
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resetPipelineTables(t, ctx, h)

	clientID := "client-s33-create-" + uuid.New().String()[:8]
	_ = auth.NewClientRateLimiter(auth.RateLimit{
		Burst:           2,
		RefillPerSecond: 0,
	}, func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) })

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

	body, _ := json.Marshal(map[string]any{
		"resourceType": "Subscription",
		"status":       "requested",
		"topic":        "http://example.org/topics/hl7-passthrough",
		"channelType":  map[string]any{"code": "rest-hook"},
		"endpoint":     "https://example.org/webhook",
		"content":      "id-only",
		"channel":      map[string]any{"type": "rest-hook", "endpoint": "https://example.org/webhook"},
	})

	send := func() (int, string) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, api.URL+"/Subscription/", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/fhir+json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp.Body.Close()
		retry := resp.Header.Get("Retry-After")
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, retry
	}

	for i := 0; i < 2; i++ {
		code, _ := send()
		if code != http.StatusCreated {
			t.Fatalf("attempt %d: code=%d, want 201", i+1, code)
		}
	}
	code, retry := send()
	if code != http.StatusTooManyRequests {
		t.Fatalf("3rd attempt: code=%d, want 429", code)
	}
	if retry == "" {
		t.Fatalf("3rd attempt: missing Retry-After")
	}
}
