// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

// OpenProject story #236 — End-to-end coverage for the prod-binary
// rest-hook activation handshake: a real POST /Subscription against
// the docker-compose-deployed binary triggers a real handshake POST
// against the test-resthook-subscriber container, and the binary's
// stored Subscription row flips from "requested" to "active".
//
// No in-process channel substitution. No e2e/harness fakes. The bytes
// flow through the real binary, through the real activator's HTTP
// client, into the real subscriber container, and the real Postgres
// row is the assertion target.
package realstack_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/realstack"
)

// TestRealStack_ProdBinary_RestHookHandshake_PostAndActivate is the
// gold-path acceptance test for #236. It:
//
//  1. Boots the realstack with HTTP-allowed url validator so the
//     activator can dial the test-resthook-subscriber's plain-HTTP
//     127.0.0.1:<port> address.
//  2. Mints a real bearer token via the test-token-mint helper.
//  3. POSTs a Subscription whose channel.endpoint points at the
//     test-resthook-subscriber's /notify/{id} URL.
//  4. Waits for the binary's activator goroutine to perform the
//     handshake POST against the subscriber.
//  5. Asserts the subscriber actually received a Bundle whose
//     SubscriptionStatus.type=="handshake".
//  6. Asserts the Postgres row's status flipped from "requested" to
//     "active" (the prod binary's audit trail of a successful
//     handshake).
func TestRealStack_ProdBinary_RestHookHandshake_PostAndActivate(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), bootTimeout)
	defer cancel()

	stack := realstack.Boot(ctx, t, realstack.Options{
		// The test-resthook-subscriber publishes on 127.0.0.1:<port> with
		// plain http://. Production keeps both off; #236 opts in.
		URLValidatorAllowHTTP:  true,
		URLValidatorAllowHosts: []string{"127.0.0.1"},
	})
	t.Cleanup(stack.Close)

	tok, err := stack.MintTestToken(ctx, nil)
	if err != nil {
		t.Fatalf("MintTestToken: %v", err)
	}

	// The endpoint the bridge will POST the handshake to. We tag the
	// path so the subscriber's journal lets us isolate this test's
	// deliveries from any other concurrent harness use.
	tag := fmt.Sprintf("h236-%d", time.Now().UnixNano())
	endpoint := stack.RestHookSubscriber.QueryAPIURL + "/notify/" + tag

	subBody := map[string]any{
		"resourceType": "Subscription",
		"status":       "requested",
		"topic":        "http://example.org/topics/h236",
		"content":      "full-resource",
		"channel": map[string]any{
			"type":     "rest-hook",
			"endpoint": endpoint,
		},
	}
	encoded, err := json.Marshal(subBody)
	if err != nil {
		t.Fatalf("marshal subscription: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		stack.Binary.URL+"/Subscription", bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("Accept", "application/fhir+json")
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /Subscription: %v", err)
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /Subscription: status %d body=%s", resp.StatusCode, string(respBody))
	}
	subID := strings.TrimPrefix(resp.Header.Get("Location"), "/Subscription/")
	if subID == "" {
		var parsed struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(respBody, &parsed)
		subID = parsed.ID
	}
	if subID == "" {
		t.Fatalf("no Subscription id returned (Location header empty, body=%s)", string(respBody))
	}

	// Poll the subscriber's journal until the handshake delivery shows
	// up. The activator runs asynchronously in the binary.
	deadline := time.Now().Add(20 * time.Second)
	var handshake *receivedRequest
	for time.Now().Before(deadline) {
		if hs, err := findHandshakeDelivery(ctx, stack.RestHookSubscriber.QueryAPIURL, tag); err != nil {
			t.Fatalf("query subscriber journal: %v", err)
		} else if hs != nil {
			handshake = hs
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context cancelled while waiting for handshake: %v", ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
	if handshake == nil {
		t.Fatalf("test-resthook-subscriber never received a handshake delivery for tag %q within 20s", tag)
	}
	if !strings.Contains(handshake.Body, `"type":"handshake"`) &&
		!strings.Contains(handshake.Body, `"type": "handshake"`) {
		t.Errorf("handshake bundle does not carry SubscriptionStatus.type=handshake; body=%s", handshake.Body)
	}

	// The prod binary should have flipped the row from requested to
	// active once the subscriber returned 200. Poll Postgres directly
	// — that is the audit-trail surface the activator updates.
	if err := waitForSubscriptionStatus(ctx, stack.Postgres.URL, subID, "active", 10*time.Second); err != nil {
		t.Fatalf("subscription %s did not become active: %v", subID, err)
	}
}

// TestRealStack_ProdBinary_RestHookHandshake_FailedFlipsToError is the
// negative-path counterpart. When the subscriber returns 5xx for the
// handshake POST, the prod binary's activator records the failure and
// leaves the Subscription row at status=error (not stuck at
// requested). This pins the failure surface that operators page on.
func TestRealStack_ProdBinary_RestHookHandshake_FailedFlipsToError(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), bootTimeout)
	defer cancel()

	stack := realstack.Boot(ctx, t, realstack.Options{
		URLValidatorAllowHTTP:  true,
		URLValidatorAllowHosts: []string{"127.0.0.1"},
	})
	t.Cleanup(stack.Close)

	tok, err := stack.MintTestToken(ctx, nil)
	if err != nil {
		t.Fatalf("MintTestToken: %v", err)
	}

	tag := fmt.Sprintf("h236-fail-%d", time.Now().UnixNano())

	// Install a program that returns 503 for every delivery to this
	// tag. The first delivery is the handshake; the activator must
	// surface the failure.
	prog := []byte(`{"sequence":[{"status":503}],"default_status":503}`)
	installURL := stack.RestHookSubscriber.ControlAPIURL + "/program/" + tag
	pResp, pErr := http.Post(installURL, "application/json", bytes.NewReader(prog))
	if pErr != nil {
		t.Fatalf("install program: %v", pErr)
	}
	pResp.Body.Close()
	if pResp.StatusCode >= 400 {
		t.Fatalf("install program: status %d", pResp.StatusCode)
	}

	endpoint := stack.RestHookSubscriber.QueryAPIURL + "/notify/" + tag
	subBody := map[string]any{
		"resourceType": "Subscription",
		"status":       "requested",
		"topic":        "http://example.org/topics/h236-fail",
		"content":      "full-resource",
		"channel": map[string]any{
			"type":     "rest-hook",
			"endpoint": endpoint,
		},
	}
	encoded, _ := json.Marshal(subBody)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		stack.Binary.URL+"/Subscription", bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /Subscription: %v", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /Subscription: status %d body=%s", resp.StatusCode, string(body))
	}
	subID := strings.TrimPrefix(resp.Header.Get("Location"), "/Subscription/")
	if subID == "" {
		var parsed struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(body, &parsed)
		subID = parsed.ID
	}
	if subID == "" {
		t.Fatalf("no Subscription id returned")
	}

	// Status must flip to error (not stay at requested).
	if err := waitForSubscriptionStatus(ctx, stack.Postgres.URL, subID, "error", 10*time.Second); err != nil {
		t.Fatalf("subscription %s did not flip to error after 503 handshake: %v", subID, err)
	}
}

// receivedRequest mirrors cmd/test-resthook-subscriber's
// ReceivedRequest shape for JSON decoding.
type receivedRequest struct {
	SubscriptionID string    `json:"subscription_id"`
	ReceivedAt     time.Time `json:"received_at"`
	Method         string    `json:"method"`
	Path           string    `json:"path"`
	Body           string    `json:"body"`
}

// findHandshakeDelivery queries the subscriber's filtered notifications
// endpoint and returns the first delivery whose body looks like a FHIR
// handshake Bundle. Returns nil when none have been observed yet.
func findHandshakeDelivery(ctx context.Context, queryURL, tag string) (*receivedRequest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, queryURL+"/notifications/"+tag, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("GET %s/notifications/%s: %d %s", queryURL, tag, resp.StatusCode, body)
	}
	var got []receivedRequest
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	for i := range got {
		if got[i].Method == http.MethodPost && strings.Contains(got[i].Body, "handshake") {
			return &got[i], nil
		}
	}
	return nil, nil
}

// waitForSubscriptionStatus polls Postgres until the subscriptions row
// for id reports the given status, or timeout elapses.
func waitForSubscriptionStatus(ctx context.Context, pgURL, id, want string, timeout time.Duration) error {
	conn, err := pgx.Connect(ctx, pgURL)
	if err != nil {
		return fmt.Errorf("pgx connect: %w", err)
	}
	defer conn.Close(ctx)

	deadline := time.Now().Add(timeout)
	var got string
	for time.Now().Before(deadline) {
		err := conn.QueryRow(ctx, `SELECT status::text FROM subscriptions WHERE id = $1`, id).Scan(&got)
		if err == nil && got == want {
			return nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("query: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("status never became %q (last seen %q)", want, got)
}
