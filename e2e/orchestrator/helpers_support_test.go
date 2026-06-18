// Copyright the fhir-subscriptions-foss authors.
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
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/fhir-subscriptions-foss/fhir-subs/e2e/mocksub"
)

// RegisterSubscriberOptions parameterizes RegisterSubscriber.
type RegisterSubscriberOptions struct {
	ClientID    string
	TopicURL    string
	ChannelType string // default "rest-hook"
	Endpoint    string
}

// RegisterSubscriber inserts the auth_clients row (idempotent — uses
// ON CONFLICT DO NOTHING) and a fresh subscriptions row pointing at the
// given topic/endpoint. Returns the new subscription id.
func RegisterSubscriber(ctx context.Context, h *Harness, opts RegisterSubscriberOptions) (string, error) {
	if h == nil || h.DB == nil {
		return "", fmt.Errorf("RegisterSubscriber: harness not initialized")
	}
	if opts.ChannelType == "" {
		opts.ChannelType = "rest-hook"
	}

	if _, err := h.DB.Exec(ctx, `
		insert into auth_clients (id, scopes, display_name)
		values ($1, ARRAY['user/*.r']::text[], $1)
		on conflict (id) do nothing
	`, opts.ClientID); err != nil {
		return "", fmt.Errorf("insert auth_client: %w", err)
	}

	subID := uuid.New()
	if _, err := h.DB.Exec(ctx, `
		insert into subscriptions
		  (id, client_id, status, topic_url, channel_type, endpoint)
		values
		  ($1, $2, 'requested', $3, $4, $5)
	`, subID, opts.ClientID, opts.TopicURL, opts.ChannelType, opts.Endpoint); err != nil {
		return "", fmt.Errorf("insert subscription: %w", err)
	}
	return subID.String(), nil
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
