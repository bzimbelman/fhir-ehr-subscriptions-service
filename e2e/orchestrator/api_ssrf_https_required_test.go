// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	hpipe "github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/harness"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
)

// staticPublicResolver pretends every host resolves to a clearly-public
// IP so we can exercise the http-vs-https policy without hitting real DNS.
type staticPublicResolver struct{}

func (staticPublicResolver) LookupIP(_ context.Context, _, _ string) ([]net.IP, error) {
	return []net.IP{net.ParseIP("93.184.216.34")}, nil
}

// TestAPISSRFHTTPSRequired rejects http://example.com/hook by default
// (HTTPS only) and accepts the same URL when the operator opts into
// AllowHTTP=true.
func TestAPISSRFHTTPSRequired(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resetPipelineTables(t, ctx, h)

	body, _ := json.Marshal(map[string]any{
		"resourceType": "Subscription",
		"status":       "requested",
		"topic":        "http://example.org/topics/hl7-passthrough",
		"channelType":  map[string]any{"code": "rest-hook"},
		"endpoint":     "http://example.com/hook",
		"content":      "id-only",
		"channel":      map[string]any{"type": "rest-hook", "endpoint": "http://example.com/hook"},
	})

	if err := seedHL7Topic(ctx, h.DB); err != nil {
		t.Fatalf("seed topic: %v", err)
	}

	t.Run("default rejects http", func(t *testing.T) {
		clientID := "client-https-default-" + uuid.New().String()[:8]
		api, err := hpipe.StartAPIServer(ctx, hpipe.APIServerConfig{
			Pool:     h.DB,
			ClientID: clientID,
			URLValidator: handlers.NewURLValidator(handlers.URLValidatorConfig{
				Resolver: staticPublicResolver{},
			}),
		})
		if err != nil {
			t.Fatalf("api start: %v", err)
		}
		t.Cleanup(func() { _ = api.Close() })

		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, api.URL+"/Subscription/", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/fhir+json")
		resp, err := api.Client().Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		respBody, _ := io.ReadAll(resp.Body)

		if resp.StatusCode != 400 {
			t.Fatalf("default policy: status=%d body=%s, want 400", resp.StatusCode, respBody)
		}
	})

	t.Run("opt-in allows http", func(t *testing.T) {
		clientID := "client-https-allow-" + uuid.New().String()[:8]
		api, err := hpipe.StartAPIServer(ctx, hpipe.APIServerConfig{
			Pool:     h.DB,
			ClientID: clientID,
			URLValidator: handlers.NewURLValidator(handlers.URLValidatorConfig{
				AllowHTTP: true,
				Resolver:  staticPublicResolver{},
			}),
		})
		if err != nil {
			t.Fatalf("api start: %v", err)
		}
		t.Cleanup(func() { _ = api.Close() })

		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, api.URL+"/Subscription/", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/fhir+json")
		resp, err := api.Client().Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		respBody, _ := io.ReadAll(resp.Body)

		if resp.StatusCode != 201 {
			t.Fatalf("opt-in policy: status=%d body=%s, want 201", resp.StatusCode, respBody)
		}
	})
}
