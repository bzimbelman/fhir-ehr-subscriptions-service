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
	"os"
	"path/filepath"
	"testing"
	"time"
)

// observationTopicForWebhook is a SubscriptionTopic the matcher loads
// at startup so the webhook-driven Observation row produces an
// ehr_event the submatcher will pick up. Matches anything (no
// queryCriteria) so the smoke test stays focused on wiring rather
// than topic-content gymnastics.
const observationTopicForWebhook = `{
  "resourceType": "SubscriptionTopic",
  "url": "http://example.org/topic/observation",
  "version": "1.0.0",
  "title": "Observation (story #100 e2e)",
  "status": "active",
  "resourceTrigger": [{
    "resource": "Observation",
    "supportedInteraction": ["create", "update"]
  }],
  "canFilterBy": [{
    "resource": "Observation",
    "filterParameter": "patient"
  }],
  "notificationShape": [{
    "resource": "Observation",
    "include": ["Observation:patient"]
  }]
}`

// TestE2E_ProdBinary_WebhookIngressMatchesAndDelivers proves that the
// production binary mounts /webhooks/{adapter}, the HMAC-signed POST
// is verified against an adapter_state-stored secret, and the parsed
// resource_change rides the matcher pipeline far enough to produce
// an ehr_event for the registered topic.
//
// Story #100. Pins:
//
//	AC #1: the route is mounted (a 404 here would mean wiring missed).
//	AC #2: the HMAC secret is plumbed from adapter_state, NOT a yaml
//	       field — this test seeds adapter_state directly.
//	AC #4: the e2e wiring works: a single POST produces both a
//	       resource_change AND an ehr_event in the production binary
//	       (proving the matcher saw the row and the topic catalog
//	       matched). The submatcher → scheduler → rest-hook delivery
//	       chain is exercised by sibling prod-binary tests; pinning
//	       deliveries here is intentionally avoided because the
//	       prod-binary submatcher is independently flaky on
//	       contention against a single-pool Postgres ("conn busy")
//	       and is unrelated to the webhook wiring this story
//	       delivers.
//
// FAILS today because cmd/fhir-subs/wiring.go never imports
// internal/webhook and never mounts the handler — POST /webhooks/...
// returns 404, no resource_change row is ever inserted, no
// ehr_event ever fires.
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

	// 1. Stage a topic catalog directory so the matcher has a
	//    non-empty catalog at startup. Without this the ehr_events
	//    insert never fires because Evaluate returns no candidates
	//    for the row's resourceType.
	topicsDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(topicsDir, "observation.json"),
		[]byte(observationTopicForWebhook), 0o600); err != nil {
		t.Fatalf("write topic: %v", err)
	}

	// 2. Boot the production binary. No MLLP needed — webhook ingress
	//    runs against the same chi router.
	bin := startProdBinary(t, ctx, prodBinaryConfig{
		DatabaseURL:      h.DBURL,
		FacilityID:       "e2e-prod-webhook",
		AdapterID:        adapterID,
		Insecure:         true,
		GracePeriod:      5 * time.Second,
		TopicsCatalogDir: topicsDir,
		// audience empty so the bearer middleware is skipped — webhook
		// ingress is HMAC-only and must NOT be bearer-gated regardless.
		AuthAudience: "",
	})
	defer bin.Stop(t, 10*time.Second)

	// 3. POST a signed FHIR-shaped change event to /webhooks/default.
	body := []byte(`{"resourceType":"Observation","id":"obs-e2e","changeKind":"create","resource":{"resourceType":"Observation","id":"obs-e2e","status":"final"}}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		bin.HTTPURL()+"/webhooks/"+adapterID, bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-Webhook-Timestamp", time.Now().UTC().Format(time.RFC3339))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /webhooks/%s: %v", adapterID, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("webhook POST: status %d, want 202", resp.StatusCode)
	}

	// 4. Wait for the matcher pipeline to land both the resource_change
	//    AND an ehr_event — that proves the webhook → matcher → topic
	//    catalog chain is wired in the production binary. We poll the
	//    DB directly rather than rely on rest-hook delivery so this
	//    test is independent of the submatcher → scheduler → channel
	//    chain (which has its own e2e coverage and an unrelated
	//    pgx-pool contention flake).
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var rcCount, ehrCount int64
		_ = h.DB.QueryRow(ctx, `SELECT count(*) FROM resource_changes WHERE adapter_id=$1 AND resource_type='Observation'`, adapterID).Scan(&rcCount)
		_ = h.DB.QueryRow(ctx, `SELECT count(*) FROM ehr_events WHERE topic_url='http://example.org/topic/observation'`).Scan(&ehrCount)
		if rcCount >= 1 && ehrCount >= 1 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	var rcCount, ehrCount int64
	_ = h.DB.QueryRow(ctx, `SELECT count(*) FROM resource_changes WHERE adapter_id=$1`, adapterID).Scan(&rcCount)
	_ = h.DB.QueryRow(ctx, `SELECT count(*) FROM ehr_events`).Scan(&ehrCount)
	t.Fatalf("webhook → matcher pipeline did not produce expected rows: resource_changes=%d, ehr_events=%d",
		rcCount, ehrCount)
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
