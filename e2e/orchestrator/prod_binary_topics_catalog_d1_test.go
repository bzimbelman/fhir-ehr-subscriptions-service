// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestE2E_ProdBinary_D1_TopicsLoadedFromCatalogDir proves that the
// production binary populates the matcher's CatalogProvider from the
// operator-supplied topics.catalog_dir at startup. Before D-1 the
// production wiring called catalog.Load(catalog.Sources{}) and the
// matcher silently received an empty catalog regardless of what the
// operator mounted.
//
// The assertion is observable from outside the binary: a startup log
// line names the loaded topic count > 0 and the operator dir.
//
// D-1.
func TestE2E_ProdBinary_D1_TopicsLoadedFromCatalogDir(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)

	topicsDir := t.TempDir()
	body := []byte(minimalServiceRequestTopic)
	if err := os.WriteFile(filepath.Join(topicsDir, "order-changed.json"), body, 0o600); err != nil {
		t.Fatalf("write topic: %v", err)
	}

	bin := startProdBinary(t, ctx, prodBinaryConfig{
		DatabaseURL:           h.DBURL,
		FacilityID:            "e2e-prod-d1",
		AdapterID:             "default",
		Insecure:              true,
		GracePeriod:           5 * time.Second,
		AuthAudience:          "https://api.test.local",
		AuthAllowInsecureJWKS: true,
		TopicsCatalogDir:      topicsDir,
	})
	defer bin.Stop(t, 5*time.Second)

	// Wait briefly for the startup log line to land.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if bin.Stderr().ContainsLine(`"topics":1`) || bin.Stderr().ContainsLine("\"topics\":1,") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, l := range bin.Stderr().Lines() {
		t.Logf("captured: %s", l)
	}
	t.Fatalf("startup did not log a non-empty topic catalog (D-1: catalog.Load with empty Sources)")
}

// TestE2E_ProdBinary_D1_SIGHUPReloadsCatalog asserts that a SIGHUP after
// a new topic file lands in topics.catalog_dir triggers a hot reload —
// the in-memory CatalogProvider swap, not a process restart. Operators
// roll out new topic mappings this way.
//
// D-1.
func TestE2E_ProdBinary_D1_SIGHUPReloadsCatalog(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)

	topicsDir := t.TempDir()

	bin := startProdBinary(t, ctx, prodBinaryConfig{
		DatabaseURL:           h.DBURL,
		FacilityID:            "e2e-prod-d1-hup",
		AdapterID:             "default",
		Insecure:              true,
		GracePeriod:           5 * time.Second,
		AuthAudience:          "https://api.test.local",
		AuthAllowInsecureJWKS: true,
		TopicsCatalogDir:      topicsDir,
	})
	defer bin.Stop(t, 5*time.Second)

	// Initial: empty dir → 0 topics.
	body := []byte(minimalServiceRequestTopic)
	if err := os.WriteFile(filepath.Join(topicsDir, "order-changed.json"), body, 0o600); err != nil {
		t.Fatalf("write topic: %v", err)
	}
	bin.SignalHUP(t)

	// Wait for the reload log line.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if bin.Stderr().ContainsLine("topic catalog reloaded") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, l := range bin.Stderr().Lines() {
		t.Logf("captured: %s", l)
	}
	t.Fatalf("SIGHUP did not trigger a 'topic catalog reloaded' log line")
}

// minimalServiceRequestTopic is a syntactically valid SubscriptionTopic
// body the catalog package compiles cleanly. The exact resource it
// triggers on (ServiceRequest) doesn't matter for the catalog-loaded
// assertion; only that the JSON Schema accepts it.
const minimalServiceRequestTopic = `{
  "resourceType": "SubscriptionTopic",
  "url": "http://example.org/topics/order-changed-d1",
  "version": "1.0.0",
  "title": "Order changed (D-1)",
  "status": "active",
  "resourceTrigger": [{
    "resource": "ServiceRequest",
    "supportedInteraction": ["create", "update"],
    "queryCriteria": {
      "previous": "status:not=active",
      "current": "status=active",
      "requireBoth": false
    },
    "fhirPathCriteria": "ServiceRequest.status.exists()"
  }],
  "canFilterBy": [{
    "resource": "ServiceRequest",
    "filterParameter": "patient"
  }],
  "notificationShape": [{
    "resource": "ServiceRequest",
    "include": ["ServiceRequest:patient"]
  }]
}`
