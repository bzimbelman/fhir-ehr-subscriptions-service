// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/mocksub"
)

// RegisterSubscriberOptions parameterizes RegisterSubscriber.
type RegisterSubscriberOptions struct {
	ClientID    string
	TopicURL    string
	ChannelType string // default "rest-hook"
	Endpoint    string

	// APIBaseURL is the base URL of a running fhir-subs binary (or a
	// test server that imitates the same surface). RegisterSubscriber
	// POSTs the FHIR Subscription resource at APIBaseURL + "/Subscription/"
	// and polls the returned id until status=active. Required.
	APIBaseURL string

	// ActivateTimeout caps how long the helper waits for the API to
	// flip the subscription from `requested` to `active`. Defaults to
	// 5s when zero.
	ActivateTimeout time.Duration
}

// RegisterSubscriber drives the production /Subscription HTTP API:
//
//  1. POST /Subscription with a FHIR Subscription resource (status
//     "requested", channelType+endpoint per opts), authenticated via
//     the dev `X-Client-Id` header so the binary's devPrincipalMiddleware
//     attaches a permissive principal.
//  2. Read the new id from the Location header (or the response body).
//  3. Poll GET /Subscription/{id} until status="active" or the
//     ActivateTimeout deadline elapses.
//
// The helper does NOT touch the database. The handler path runs every
// validation gate the production binary runs in production: schema
// validation, topic-catalog lookup, channel registry, SSRF guard,
// activation handshake. A test that calls RegisterSubscriber and then
// observes the row appear active is observing the real wiring.
//
// The Harness pointer is retained for symmetry with the other helpers
// (and may be nil, e.g. in unit tests that drive only the HTTP path).
func RegisterSubscriber(ctx context.Context, _ *Harness, opts RegisterSubscriberOptions) (string, error) {
	if opts.APIBaseURL == "" {
		return "", fmt.Errorf("RegisterSubscriber: APIBaseURL is required (no SQL bypass — story #150)")
	}
	if opts.ChannelType == "" {
		opts.ChannelType = "rest-hook"
	}
	if opts.ActivateTimeout == 0 {
		opts.ActivateTimeout = 5 * time.Second
	}

	body, err := buildSubscriptionBody(opts)
	if err != nil {
		return "", err
	}

	postURL := strings.TrimRight(opts.APIBaseURL, "/") + "/Subscription/"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, postURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("RegisterSubscriber: build POST: %w", err)
	}
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("Accept", "application/fhir+json")
	req.Header.Set("X-Client-Id", opts.ClientID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("RegisterSubscriber: POST /Subscription: %w", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("RegisterSubscriber: POST /Subscription status=%d body=%s",
			resp.StatusCode, truncateBody(respBody))
	}

	subID, err := extractSubscriptionID(resp.Header.Get("Location"), respBody)
	if err != nil {
		return "", fmt.Errorf("RegisterSubscriber: %w", err)
	}

	if err := waitForSubscriptionActive(ctx, opts.APIBaseURL, opts.ClientID, subID, opts.ActivateTimeout); err != nil {
		return "", fmt.Errorf("RegisterSubscriber: %w", err)
	}
	return subID, nil
}

// buildSubscriptionBody marshals a Subscription payload that satisfies
// the production binary's schema (R4B backport: requires `channel`)
// AND the R5 fields the parser reads (channelType, endpoint). The two
// shapes overlap in the wild; sending both keeps the helper compatible
// with whichever form the parser pulls first.
func buildSubscriptionBody(opts RegisterSubscriberOptions) ([]byte, error) {
	doc := map[string]any{
		"resourceType": "Subscription",
		"status":       "requested",
		"topic":        opts.TopicURL,
		"channelType": map[string]any{
			"system": "http://terminology.hl7.org/CodeSystem/subscription-channel-type",
			"code":   opts.ChannelType,
		},
		"endpoint":    opts.Endpoint,
		"contentType": "application/fhir+json",
		"content":     "id-only",
		"channel": map[string]any{
			"type":     opts.ChannelType,
			"endpoint": opts.Endpoint,
			"payload":  "application/fhir+json",
		},
	}
	return json.Marshal(doc)
}

// extractSubscriptionID prefers the Location header (the FHIR-canonical
// place to publish the new resource id) and falls back to parsing the
// returned Subscription body.
func extractSubscriptionID(location string, body []byte) (string, error) {
	if location != "" {
		base := path.Base(location)
		if base == "1" || strings.HasPrefix(base, "_history") {
			parts := strings.Split(strings.Trim(location, "/"), "/")
			for i := len(parts) - 1; i >= 0; i-- {
				if parts[i] == "" || parts[i] == "1" || strings.HasPrefix(parts[i], "_history") {
					continue
				}
				return parts[i], nil
			}
		}
		if base != "" && base != "/" && base != "Subscription" {
			return base, nil
		}
	}
	var doc struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("decode Subscription response: %w body=%s", err, truncateBody(body))
	}
	if doc.ID == "" {
		return "", fmt.Errorf("Subscription response missing id; body=%s", truncateBody(body))
	}
	return doc.ID, nil
}

// waitForSubscriptionActive polls GET /Subscription/{id} until the
// returned status is "active" or the deadline elapses. Returns an error
// describing the last observed status on timeout.
func waitForSubscriptionActive(ctx context.Context, apiBase, clientID, subID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	getURL := strings.TrimRight(apiBase, "/") + "/Subscription/" + subID
	var lastStatus string
	var lastBody string
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, getURL, nil)
		if err != nil {
			return fmt.Errorf("build GET /Subscription/%s: %w", subID, err)
		}
		req.Header.Set("Accept", "application/fhir+json")
		req.Header.Set("X-Client-Id", clientID)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			if time.Now().After(deadline) {
				return fmt.Errorf("GET /Subscription/%s: %w", subID, err)
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		lastBody = string(body)
		if resp.StatusCode == http.StatusOK {
			var doc struct {
				Status string `json:"status"`
			}
			if jerr := json.Unmarshal(body, &doc); jerr == nil {
				lastStatus = doc.Status
				if doc.Status == "active" {
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("subscription %s not active within %s (last status=%q body=%s)",
				subID, timeout, lastStatus, truncateBody([]byte(lastBody)))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func truncateBody(b []byte) string {
	const max = 256
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "...(truncated)"
}

// WaitForNotification polls the mocksub assertion endpoint until a
// matching notification appears, or returns an error on timeout. The
// returned ReceivedNotification is the matched journal entry.
func WaitForNotification(ctx context.Context, h *Harness, subscriptionID string, timeout time.Duration) (*mocksub.ReceivedNotification, error) {
	if h == nil || h.MockSub == nil {
		return nil, fmt.Errorf("WaitForNotification: harness not initialized")
	}
	url := fmt.Sprintf("http://%s/assert/notification_received", h.MockSub.HTTPAddr)
	body := map[string]any{
		"subscription_id": subscriptionID,
		"timeout_ms":      int(timeout / time.Millisecond),
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusRequestTimeout {
		return nil, fmt.Errorf("timed out waiting for notification on subscription %s", subscriptionID)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("WaitForNotification: status %d body=%s", resp.StatusCode, respBody)
	}
	var got mocksub.ReceivedNotification
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		return nil, fmt.Errorf("decode notification: %w", err)
	}
	return &got, nil
}

// ResourceChangeRow is the subset of the resource_changes contract that
// orchestrator tests assert against.
type ResourceChangeRow struct {
	ID            string
	AdapterID     string
	CorrelationID string
	ResourceType  string
	ChangeKind    string
	Resource      []byte
}

// AssertResourceChanges returns rows in resource_changes for the given
// (adapter_id, correlation_id) pair. Returns the slice (possibly empty)
// or an error on query failure.
func AssertResourceChanges(ctx context.Context, h *Harness, adapterID, correlationID string) ([]ResourceChangeRow, error) {
	if h == nil || h.DB == nil {
		return nil, fmt.Errorf("AssertResourceChanges: harness not initialized")
	}
	rows, err := h.DB.Query(ctx, `
		select id, adapter_id, correlation_id::text, resource_type,
		       change_kind, resource
		  from resource_changes
		 where adapter_id = $1 and correlation_id = $2::uuid
	`, adapterID, correlationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ResourceChangeRow
	for rows.Next() {
		var r ResourceChangeRow
		if err := rows.Scan(&r.ID, &r.AdapterID, &r.CorrelationID,
			&r.ResourceType, &r.ChangeKind, &r.Resource); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// bytesReader is a tiny adapter so InjectNotification can stay in
// setup_test.go without importing bytes there.
func bytesReader(b []byte) io.Reader {
	return bytes.NewReader(b)
}

// QueueSize returns the number of rows in hl7_message_queue. Used by the
// smoke_persist scenario to assert that one message persists.
func (h *Harness) QueueSize(ctx context.Context) (int, error) {
	var n int
	err := h.DB.QueryRow(ctx, `select count(*) from hl7_message_queue`).Scan(&n)
	return n, err
}

// postScenario POSTs a JSON body to the EHR mock's scenario control plane
// and returns the response body. Fails the test on any transport error
// or non-2xx status.
func postScenario(t *testing.T, ctx context.Context, h *Harness, path string, body any) []byte {
	t.Helper()
	url := fmt.Sprintf("http://%s%s", h.MockEHR.HTTPAddr, path)
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("post %s: status %d body=%s", path, resp.StatusCode, respBody)
	}
	return respBody
}
