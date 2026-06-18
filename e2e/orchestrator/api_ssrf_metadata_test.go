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
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
)

// TestAPISSRFMetadata posts a Subscription whose endpoint points at the
// AWS metadata IP. Without the SSRF guard the rest-hook channel adapter
// would receive `169.254.169.254` and gladly fire a request at it from
// inside the EKS pod. With B-11 in place, the API rejects the request
// before persistence and the channel never sees it.
func TestAPISSRFMetadata(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resetPipelineTables(t, ctx, h)

	tracker := &trackingChannel{}
	clientID := "client-ssrf-meta-" + uuid.New().String()[:8]
	api, err := hpipe.StartAPIServer(ctx, hpipe.APIServerConfig{
		Pool:          h.DB,
		ClientID:      clientID,
		URLValidator:  handlers.NewURLValidator(handlers.URLValidatorConfig{}),
		ExtraChannels: handlers.ChannelRegistry{"rest-hook": tracker},
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
		"endpoint":     "http://169.254.169.254/latest/meta-data/",
		"content":      "id-only",
		"channel":      map[string]any{"type": "rest-hook", "endpoint": "http://169.254.169.254/latest/meta-data/"},
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, api.URL+"/Subscription/", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 400 {
		t.Fatalf("status=%d body=%s, want 400", resp.StatusCode, respBody)
	}
	if got := tracker.Calls(); got != 0 {
		t.Fatalf("rest-hook activator was called %d times despite SSRF rejection", got)
	}
}
