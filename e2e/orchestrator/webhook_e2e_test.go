// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"
	"time"
)

// TestE2E_ProdBinary_WebhookIngressMatchesAndDelivers proves that the
// production binary mounts /webhooks/{adapter}, the HMAC-signed POST
// is verified against an adapter_state-stored secret, and the parsed
// resource_change rides the matcher → submatcher → scheduler → rest-hook
// pipeline to the registered subscriber.
//
// Story #100. Pins:
//   AC #1: the route is mounted (a 404 here would mean wiring missed).
//   AC #2: the HMAC secret is plumbed from adapter_state, NOT a yaml
//          field — this test seeds adapter_state directly.
//   AC #4: the e2e fan-out works: a single POST produces a synthetic
//          resource_change that the matcher → submatcher → scheduler
//          stack delivers to the rest-hook subscriber.
//
// FAILS today because cmd/fhir-subs/wiring.go never imports
// internal/webhook and never mounts the handler — POST /webhooks/...
// returns 404.
func TestE2E_ProdBinary_WebhookIngressMatchesAndDelivers(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)

	const adapterID = "default"
	const secret = "e2e-webhook-secret-v1"

	// Seed the per-adapter HMAC secret in adapter_state so the
	// production binary's resolver picks it up. AC #2: rotation is via
	// upsert into this row.
	if _, err := h.DB.Exec(ctx,
		`INSERT INTO adapter_state (adapter_id, scope, key, value, key_version, updated_at)
		 VALUES ($1, 'webhook', 'secret', $2, 1, now())
		 ON CONFLICT (adapter_id, scope, key)
		 DO UPDATE SET value = excluded.value, updated_at = now()`,
		adapterID, []byte(secret),
	); err != nil {
		t.Fatalf("seed adapter_state.webhook.secret: %v", err)
	}

	// 1. Register a subscription pointing at the harness rest-hook
	//    receiver. The submatcher's "active subscriptions" filter only
	//    sees rows in `active` status, so flip after insert.
	subID, err := RegisterSubscriber(ctx, h, RegisterSubscriberOptions{
		ClientID: "e2e-webhook-client",
		TopicURL: "http://example.org/topic/observation",
		Endpoint: "http://" + h.MockSub.HTTPAddr + "/hook/e2e-webhook-sub",
	})
	if err != nil {
		t.Fatalf("RegisterSubscriber: %v", err)
	}
	if _, err := h.DB.Exec(ctx, `UPDATE subscriptions SET status='active' WHERE id=$1`, subID); err != nil {
		t.Fatalf("activate sub: %v", err)
	}

	// 2. Boot the production binary. No MLLP needed — webhook ingress
	//    runs against the same chi router.
	bin := startProdBinary(t, ctx, prodBinaryConfig{
		DatabaseURL: h.DBURL,
		FacilityID:  "e2e-prod-webhook",
		AdapterID:   adapterID,
		Insecure:    true,
		GracePeriod: 5 * time.Second,
		// audience empty so the bearer middleware is skipped — webhook
		// ingress is HMAC-only and must NOT be bearer-gated regardless.
		AuthAudience: "",
	})
	defer bin.Stop(t, 10*time.Second)

	// 3. POST a signed FHIR-shaped change event.
	body := []byte(`{"resourceType":"Observation","id":"obs-e2e","changeKind":"create","resource":{"resourceType":"Observation","id":"obs-e2e","status":"final"}}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		bin.HTTPURL()+"/webhooks/"+adapterID, bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /webhooks/%s: %v", adapterID, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("webhook POST: status %d, want 202", resp.StatusCode)
	}

	// 4. The full pipeline must deliver to the rest-hook subscriber.
	got, err := WaitForNotification(ctx, h, "e2e-webhook-sub", 30*time.Second)
	if err != nil {
		t.Fatalf("WaitForNotification: %v", err)
	}
	if got.SubscriptionID != "e2e-webhook-sub" {
		t.Fatalf("subscription id: got %q", got.SubscriptionID)
	}
}

// TestE2E_ProdBinary_WebhookRejectsBadSignature pins AC #1 (the route
// is mounted) and AC #2 (HMAC verification is real, not a stub) by
// asserting that a POST with a deliberately wrong signature returns
// 401 from the binary, NOT 404 (route missing) and NOT 202 (signature
// path stubbed).
//
// FAILS today: route returns 404 because nothing in wiring.go calls
// webhook.NewHandler.
func TestE2E_ProdBinary_WebhookRejectsBadSignature(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)

	const adapterID = "default"
	if _, err := h.DB.Exec(ctx,
		`INSERT INTO adapter_state (adapter_id, scope, key, value, key_version, updated_at)
		 VALUES ($1, 'webhook', 'secret', $2, 1, now())
		 ON CONFLICT (adapter_id, scope, key)
		 DO UPDATE SET value = excluded.value, updated_at = now()`,
		adapterID, []byte("right-secret"),
	); err != nil {
		t.Fatalf("seed adapter_state: %v", err)
	}

	bin := startProdBinary(t, ctx, prodBinaryConfig{
		DatabaseURL:  h.DBURL,
		FacilityID:   "e2e-prod-webhook-badsig",
		AdapterID:    adapterID,
		Insecure:     true,
		GracePeriod:  5 * time.Second,
		AuthAudience: "",
	})
	defer bin.Stop(t, 10*time.Second)

	body := []byte(`{"resourceType":"Observation","id":"obs-x","changeKind":"create","resource":{"resourceType":"Observation","id":"obs-x"}}`)
	mac := hmac.New(sha256.New, []byte("WRONG-secret"))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		bin.HTTPURL()+"/webhooks/"+adapterID, bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /webhooks/%s: %v", adapterID, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Fatalf("/webhooks/%s not mounted (404) — wiring missed", adapterID)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad signature: status %d, want 401", resp.StatusCode)
	}
}
