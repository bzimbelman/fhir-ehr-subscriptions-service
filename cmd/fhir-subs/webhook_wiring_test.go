// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/lifecycle"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// Phase A (RED) tests for OpenProject story #100: production binary
// MUST wire the webhook ingress (internal/webhook) onto the public chi
// router so vendors that POST signed change events to
// /webhooks/{adapter} reach the matcher pipeline.
//
// Each test pins one acceptance criterion:
//   AC #1: /webhooks/{adapter} is mounted on the public router.
//   AC #2: HMAC secret is plumbed from adapter_state (scope=webhook,
//          key=secret) so operators rotate by upserting that row.
//   AC #3: An HMAC-valid POST with a known FHIR-shaped body produces
//          a row in resource_changes (the matcher's input).
//
// Tests are gated on TEST_PG_URL because the production wiring requires
// a real Postgres pool (storage.Start is invoked, schema is migrated,
// and the resource_changes insert hits the partitioned table).

// TestProductionRuntime_MountsWebhookRoute asserts that
// buildProductionRuntime mounts /webhooks/{adapter} on the chi router.
// FAILS today: cmd/fhir-subs/wiring.go never imports internal/webhook
// and never calls webhook.NewHandler / Mount, so the route is missing
// (404 from the chi NotFound handler).
func TestProductionRuntime_MountsWebhookRoute(t *testing.T) {
	dbURL := os.Getenv("TEST_PG_URL")
	if dbURL == "" {
		t.Skip("TEST_PG_URL not set; integration assertion runs in CI")
	}

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1"},
		Adapter:    AdapterConfig{ID: "default"},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: "127.0.0.1:0", Insecure: true}},
		Lifecycle:  LifecycleConfig{ShutdownGracePeriod: 5 * time.Second},
		Database:   DatabaseConfig{URL: dbURL},
		Codec: CodecConfig{
			ActiveKeyVersion: 1,
			Keys: []CodecKeySpec{
				{Version: 1, Material: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
			},
		},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lcMod, err := lifecycle.Start(ctx, lifecycle.LifecycleConfig{
		ShutdownGracePeriod: 5 * time.Second,
	}, lifecycle.LifecycleContext{Logger: logger})
	if err != nil {
		t.Fatalf("lifecycle.Start: %v", err)
	}

	rt, err := buildProductionRuntime(ctx, cfg, logger, lcMod)
	if err != nil {
		t.Fatalf("buildProductionRuntime: %v", err)
	}
	defer rt.shutdown(context.Background())

	// Send a POST with no signature header — the webhook handler must
	// be wired and reject with 401 (its own error path), NOT 404 (the
	// chi NotFoundHandler that fires when a route was never mounted).
	body := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/default", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	rt.router.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
		t.Fatalf("/webhooks/{adapter} not mounted on production router: got %d, want 401 (no signature)", rec.Code)
	}
}

// TestProductionRuntime_WebhookSecretFromAdapterState asserts that the
// HMAC resolver loads its secret from adapter_state. Operators rotate
// the secret by upserting (adapter_id, scope='webhook', key='secret',
// value=<plaintext>) — the resolver MUST observe the new value on the
// next request without restart.
//
// FAILS today: no resolver is wired, so a signed POST returns 404 (no
// route) instead of 202 (accepted) regardless of what's in
// adapter_state.
func TestProductionRuntime_WebhookSecretFromAdapterState(t *testing.T) {
	dbURL := os.Getenv("TEST_PG_URL")
	if dbURL == "" {
		t.Skip("TEST_PG_URL not set; integration assertion runs in CI")
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	const adapterID = "default"
	const secret = "rotation-secret-v1"
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO adapter_state (adapter_id, scope, key, value, key_version, updated_at)
		 VALUES ($1, 'webhook', 'secret', $2, 1, now())
		 ON CONFLICT (adapter_id, scope, key)
		 DO UPDATE SET value = excluded.value, updated_at = now()`,
		adapterID, []byte(secret),
	); err != nil {
		t.Fatalf("seed adapter_state: %v", err)
	}

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1"},
		Adapter:    AdapterConfig{ID: adapterID},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: "127.0.0.1:0", Insecure: true}},
		Lifecycle:  LifecycleConfig{ShutdownGracePeriod: 5 * time.Second},
		Database:   DatabaseConfig{URL: dbURL},
		Codec: CodecConfig{
			ActiveKeyVersion: 1,
			Keys: []CodecKeySpec{
				{Version: 1, Material: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
			},
		},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lcMod, err := lifecycle.Start(ctx, lifecycle.LifecycleConfig{
		ShutdownGracePeriod: 5 * time.Second,
	}, lifecycle.LifecycleContext{Logger: logger})
	if err != nil {
		t.Fatalf("lifecycle.Start: %v", err)
	}

	rt, err := buildProductionRuntime(ctx, cfg, logger, lcMod)
	if err != nil {
		t.Fatalf("buildProductionRuntime: %v", err)
	}
	defer rt.shutdown(context.Background())

	body := []byte(`{"resourceType":"ServiceRequest","id":"sr-1","changeKind":"create","resource":{"resourceType":"ServiceRequest","id":"sr-1","status":"active"}}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/webhooks/"+adapterID, bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rt.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("signed POST to /webhooks/%s: got %d body=%q, want 202",
			adapterID, rec.Code, rec.Body.String())
	}
}

// TestProductionRuntime_WebhookEnqueuesResourceChange asserts a signed
// webhook POST persists a row in resource_changes — the matcher's
// input. This pins the "makes it to the matcher" half of AC #3:
// without a resource_changes row the matcher worker has nothing to
// claim.
//
// FAILS today: the route is not mounted; even if we forced a 202 by
// stubbing, the handler.deps.Repo / Querier are not threaded by the
// production wiring, so the row would never be inserted.
func TestProductionRuntime_WebhookEnqueuesResourceChange(t *testing.T) {
	dbURL := os.Getenv("TEST_PG_URL")
	if dbURL == "" {
		t.Skip("TEST_PG_URL not set; integration assertion runs in CI")
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	const adapterID = "default"
	const secret = "enqueue-test-secret"
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO adapter_state (adapter_id, scope, key, value, key_version, updated_at)
		 VALUES ($1, 'webhook', 'secret', $2, 1, now())
		 ON CONFLICT (adapter_id, scope, key)
		 DO UPDATE SET value = excluded.value, updated_at = now()`,
		adapterID, []byte(secret),
	); err != nil {
		t.Fatalf("seed adapter_state: %v", err)
	}

	// Snapshot the current resource_changes row count for this adapter
	// so the assertion is delta-based and resilient to other tests.
	var before int64
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM resource_changes WHERE adapter_id=$1`, adapterID,
	).Scan(&before); err != nil {
		t.Fatalf("count before: %v", err)
	}

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1"},
		Adapter:    AdapterConfig{ID: adapterID},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: "127.0.0.1:0", Insecure: true}},
		Lifecycle:  LifecycleConfig{ShutdownGracePeriod: 5 * time.Second},
		Database:   DatabaseConfig{URL: dbURL},
		Codec: CodecConfig{
			ActiveKeyVersion: 1,
			Keys: []CodecKeySpec{
				{Version: 1, Material: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
			},
		},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lcMod, err := lifecycle.Start(ctx, lifecycle.LifecycleConfig{
		ShutdownGracePeriod: 5 * time.Second,
	}, lifecycle.LifecycleContext{Logger: logger})
	if err != nil {
		t.Fatalf("lifecycle.Start: %v", err)
	}

	rt, err := buildProductionRuntime(ctx, cfg, logger, lcMod)
	if err != nil {
		t.Fatalf("buildProductionRuntime: %v", err)
	}
	defer rt.shutdown(context.Background())

	body := []byte(`{"resourceType":"Observation","id":"obs-100","changeKind":"create","resource":{"resourceType":"Observation","id":"obs-100","status":"final"}}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/webhooks/"+adapterID, bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rt.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("signed POST: got %d body=%q, want 202", rec.Code, rec.Body.String())
	}

	var after int64
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM resource_changes WHERE adapter_id=$1 AND resource_type='Observation'`, adapterID,
	).Scan(&after); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if after <= before {
		t.Fatalf("resource_changes row count did not grow: before=%d after=%d", before, after)
	}

	// Confirm the row carries the expected ChangeKind so the matcher
	// path treats it as a create event.
	var kind string
	if err := pool.QueryRow(context.Background(),
		`SELECT change_kind::text FROM resource_changes
		 WHERE adapter_id=$1 AND resource_type='Observation'
		 ORDER BY created_at DESC LIMIT 1`, adapterID,
	).Scan(&kind); err != nil {
		t.Fatalf("read change_kind: %v", err)
	}
	if repos.ChangeKind(kind) != repos.ChangeCreate {
		t.Fatalf("change_kind: got %q, want %q", kind, repos.ChangeCreate)
	}
}
