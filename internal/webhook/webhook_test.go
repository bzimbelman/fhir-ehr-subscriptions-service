// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package webhook_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/webhook"
)

func sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func mountForTest(deps webhook.Deps) http.Handler {
	r := chi.NewRouter()
	r.Route("/webhooks", func(sub chi.Router) {
		webhook.NewHandler(deps).Mount(sub)
	})
	return r
}

// P2.9: missing signature header → 401.
func TestWebhook_MissingSignatureRejected(t *testing.T) {
	t.Parallel()
	h := mountForTest(webhook.Deps{
		Resolver: webhook.SecretMap{"vendorA": "shh"},
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	body := []byte(`{"resourceType":"ServiceRequest","changeKind":"create","resource":{}}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/webhooks/vendorA", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: want 401, got %d", resp.StatusCode)
	}
}

// P2.9: unknown adapter → 404.
func TestWebhook_UnknownAdapterRejected(t *testing.T) {
	t.Parallel()
	h := mountForTest(webhook.Deps{
		Resolver: webhook.SecretMap{"vendorA": "shh"},
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	body := []byte(`{}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/webhooks/vendorB", bytes.NewReader(body))
	req.Header.Set(webhook.SignatureHeader, sign(body, "shh"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: want 404, got %d", resp.StatusCode)
	}
}

// P2.9: signature computed with the wrong secret → 401.
func TestWebhook_WrongSecretRejected(t *testing.T) {
	t.Parallel()
	h := mountForTest(webhook.Deps{
		Resolver: webhook.SecretMap{"vendorA": "right-secret"},
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	body := []byte(`{"resourceType":"ServiceRequest","changeKind":"create","resource":{}}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/webhooks/vendorA", bytes.NewReader(body))
	req.Header.Set(webhook.SignatureHeader, sign(body, "wrong-secret"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: want 401, got %d", resp.StatusCode)
	}
}

// P2.9: signature scheme other than sha256= → 401.
func TestWebhook_UnsupportedSchemeRejected(t *testing.T) {
	t.Parallel()
	h := mountForTest(webhook.Deps{
		Resolver: webhook.SecretMap{"vendorA": "shh"},
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	body := []byte(`{}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/webhooks/vendorA", bytes.NewReader(body))
	req.Header.Set(webhook.SignatureHeader, "md5=abcd")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: want 401, got %d", resp.StatusCode)
	}
}

// P2.9: stale timestamp (outside skew) → 401.
func TestWebhook_StaleTimestampRejected(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	h := mountForTest(webhook.Deps{
		Resolver:     webhook.SecretMap{"vendorA": "shh"},
		Clock:        func() time.Time { return now },
		MaxClockSkew: time.Minute,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	body := []byte(`{}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/webhooks/vendorA", bytes.NewReader(body))
	req.Header.Set(webhook.SignatureHeader, sign(body, "shh"))
	// 10 minutes old.
	req.Header.Set("X-Webhook-Timestamp", now.Add(-10*time.Minute).Format(time.RFC3339))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: want 401 on stale timestamp, got %d", resp.StatusCode)
	}
}

// P2.9: malformed body json → 400.
func TestWebhook_MalformedJSONRejected(t *testing.T) {
	t.Parallel()
	h := mountForTest(webhook.Deps{
		Resolver: webhook.SecretMap{"vendorA": "shh"},
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	body := []byte(`not json`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/webhooks/vendorA", bytes.NewReader(body))
	req.Header.Set(webhook.SignatureHeader, sign(body, "shh"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: want 400, got %d", resp.StatusCode)
	}
}

// P2.9: a valid HMAC-signed request but no DB wiring returns 503 (the
// handler short-circuits before persistence). This confirms the
// signature path passes and the business logic dispatch works without
// requiring a real Postgres in this unit test.
func TestWebhook_NoRepoConfiguredReturns503(t *testing.T) {
	t.Parallel()
	h := mountForTest(webhook.Deps{
		Resolver: webhook.SecretMap{"vendorA": "shh"},
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	body := []byte(`{"resourceType":"ServiceRequest","changeKind":"create","resource":{}}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/webhooks/vendorA", bytes.NewReader(body))
	req.Header.Set(webhook.SignatureHeader, sign(body, "shh"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: want 503, got %d", resp.StatusCode)
	}
}
