// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/realstack"
)

// rsScenarioCtx wraps a per-test realstack.Stack so each scenario reads
// top-down without re-passing ctx + stack everywhere.
type rsScenarioCtx struct {
	ctx   context.Context
	t     *testing.T
	stack *realstack.Stack
}

// bootForScenario brings up the H1 stack with the options every scenario
// shares. Tests that need MLLP or specialized config opt in via opts.
func bootForScenario(t *testing.T, opts realstack.Options) *rsScenarioCtx {
	t.Helper()
	if err := realstack.CheckDocker(); err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	stack := realstack.Boot(ctx, t, opts)
	t.Cleanup(stack.Close)

	resetSubscriberJournals(t, stack)
	return &rsScenarioCtx{ctx: ctx, t: t, stack: stack}
}

// resetSubscriberJournals clears the rest-hook + ws subscriber capture
// journals so per-test assertions only see the deliveries this test
// caused. The two test subscriber binaries expose POST /reset.
func resetSubscriberJournals(t *testing.T, stack *realstack.Stack) {
	t.Helper()
	for _, base := range []string{
		stack.RestHookSubscriber.QueryAPIURL,
		stack.WSSubscriber.QueryAPIURL,
	} {
		if base == "" {
			continue
		}
		req, _ := http.NewRequest(http.MethodPost, base+"/reset", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Logf("reset %s: %v", base, err)
			continue
		}
		_ = resp.Body.Close()
	}
}

// postSubscription drives the prod binary's POST /Subscription with the
// given FHIR JSON body. Returns the new subscription id parsed from the
// Location header. Fails the test on non-2xx.
func (s *rsScenarioCtx) postSubscription(body []byte) string {
	s.t.Helper()
	req, _ := http.NewRequestWithContext(s.ctx, http.MethodPost,
		s.stack.Binary.URL+"/Subscription", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatalf("POST /Subscription: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		rb, _ := io.ReadAll(resp.Body)
		s.t.Fatalf("POST /Subscription: %d %s", resp.StatusCode, string(rb))
	}
	loc := resp.Header.Get("Location")
	return strings.TrimPrefix(loc, "/Subscription/")
}

// buildSubscriptionWithEndpoint returns a FHIR Subscription resource
// targeting the given topic + endpoint URL. Used by scenarios that point
// the subscription at non-realstack endpoints (e.g. dead-drop addresses
// for dead-letter testing).
func buildSubscriptionWithEndpoint(topicURL, endpoint string) []byte {
	body, _ := json.Marshal(map[string]any{
		"resourceType": "Subscription",
		"status":       "requested",
		"topic":        topicURL,
		"channelType":  map[string]any{"code": "rest-hook"},
		"endpoint":     endpoint,
		"content":      "id-only",
		"channel": map[string]any{
			"type":     "rest-hook",
			"endpoint": endpoint,
		},
	})
	return body
}

// restHookSubscriptionJSON returns a FHIR Subscription resource pointing
// at the realstack rest-hook test subscriber under a distinct subscription
// path, so the captured journal can filter by tag.
func restHookSubscriptionJSON(stack *realstack.Stack, topicURL, tag string) []byte {
	endpoint := stack.RestHookSubscriber.EndpointURL + "/" + tag
	body, _ := json.Marshal(map[string]any{
		"resourceType": "Subscription",
		"status":       "requested",
		"topic":        topicURL,
		"channelType":  map[string]any{"code": "rest-hook"},
		"endpoint":     endpoint,
		"content":      "id-only",
		"channel": map[string]any{
			"type":     "rest-hook",
			"endpoint": endpoint,
		},
	})
	return body
}

// restHookNotifications fetches the captured notifications for the given
// subscription tag from the test rest-hook subscriber binary.
func (s *rsScenarioCtx) restHookNotifications(tag string) []capturedRequest {
	s.t.Helper()
	url := s.stack.RestHookSubscriber.QueryAPIURL + "/notifications/" + tag
	req, _ := http.NewRequestWithContext(s.ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		s.t.Fatalf("GET %s: %d", url, resp.StatusCode)
	}
	var out []capturedRequest
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		s.t.Fatalf("decode notifications: %v", err)
	}
	return out
}

// waitForRestHookNotifications polls the rest-hook subscriber until it
// reports at least min captured deliveries for the given tag, or timeout
// elapses. Returns the final journal snapshot.
func (s *rsScenarioCtx) waitForRestHookNotifications(tag string, min int, timeout time.Duration) []capturedRequest {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	var last []capturedRequest
	for time.Now().Before(deadline) {
		last = s.restHookNotifications(tag)
		if len(last) >= min {
			return last
		}
		time.Sleep(200 * time.Millisecond)
	}
	return last
}

// capturedRequest mirrors the test-resthook-subscriber's ReceivedRequest
// shape for easy decoding.
type capturedRequest struct {
	SubscriptionID string            `json:"subscription_id"`
	ReceivedAt     time.Time         `json:"received_at"`
	Method         string            `json:"method"`
	Path           string            `json:"path"`
	Header         http.Header       `json:"header"`
	Body           string            `json:"body"`
}

// hapiPostResource creates a FHIR resource on the real HAPI FHIR server
// inside the realstack. Used by FHIR-scan-driven scenarios to seed the
// upstream EHR with the resource the binary will scan.
func (s *rsScenarioCtx) hapiPostResource(resourceType string, resource map[string]any) {
	s.t.Helper()
	body, _ := json.Marshal(resource)
	url := s.stack.HAPIFHIR.BaseURL + "/" + resourceType
	req, _ := http.NewRequestWithContext(s.ctx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		rb, _ := io.ReadAll(resp.Body)
		s.t.Fatalf("POST %s: %d %s", url, resp.StatusCode, string(rb))
	}
}

// binaryGet issues an authenticated GET against the prod binary's API.
// The binary is launched with auth.allow_dev_bypass=true in the harness
// so the bearer header is informational; tests that exercise auth
// negative paths set their own headers.
func (s *rsScenarioCtx) binaryGet(path string) (*http.Response, []byte) {
	s.t.Helper()
	url := s.stack.Binary.URL + path
	req, _ := http.NewRequestWithContext(s.ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatalf("GET %s: %v", url, err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, body
}

// shortTagFor returns a per-test stable tag the rest-hook receiver can
// filter on. Format: <test name lower-snake>-<8 hex>.
func shortTagFor(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("rs-%s-%d", strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_")), time.Now().UnixNano()%0xffffffff)
}
