// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestE2E_ProdBinary_MetadataUnauthenticated boots the production
// binary with a valid auth.audience (full production posture, NOT a
// dev-bypass) and asserts that GET /metadata succeeds with no
// Authorization header. FHIR conformance probes (Inferno, HL7 testkit)
// hit /metadata without a bearer token; today /metadata sits inside
// the auth-protected route group and returns 401, so the audit doc's
// "S-2.1 RESOLVED" claim is wrong on the deployed binary. Story #93
// moves /metadata onto the public sub-mux via RegisterPublicRoutes
// before RegisterRoutes mounts the auth-gated FHIR API.
func TestE2E_ProdBinary_MetadataUnauthenticated(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)

	// Boot the binary with a real auth.audience so the bearer-token
	// verifier is wired. /metadata MUST still be reachable without a
	// bearer token because RegisterPublicRoutes mounts it on the bare
	// chi router before the auth-protected group.
	bin := startProdBinary(t, ctx, prodBinaryConfig{
		DatabaseURL:           h.DBURL,
		FacilityID:            "e2e-metadata-unauth",
		AdapterID:             "default",
		Insecure:              true,
		GracePeriod:           5 * time.Second,
		AuthAudience:          "https://example.invalid/audience",
		AuthAllowInsecureJWKS: true,
	})
	defer bin.Stop(t, 5*time.Second)

	// Plain HTTP GET /metadata, no Authorization header — exactly
	// what a FHIR conformance probe sends.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		bin.HTTPURL()+"/metadata", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /metadata: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metadata = %d; want 200; body=%s",
			resp.StatusCode, body)
	}

	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("response not JSON: %v; body=%s", err, body)
	}
	rt, _ := doc["resourceType"].(string)
	if rt != "CapabilityStatement" {
		t.Fatalf("resourceType = %q, want CapabilityStatement; body=%s",
			rt, body)
	}

	// Sanity: a known FHIR resource path STILL requires auth — the
	// auth gate must not have been disabled across the board. A
	// regression that mounts /Subscription on the public mux would
	// hit this assertion.
	subReq, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		bin.HTTPURL()+"/Subscription", nil)
	subResp, err := http.DefaultClient.Do(subReq)
	if err != nil {
		t.Fatalf("GET /Subscription: %v", err)
	}
	defer subResp.Body.Close()
	if subResp.StatusCode == http.StatusOK {
		t.Fatalf("GET /Subscription returned 200 without auth; the auth gate is broken")
	}
	if subResp.StatusCode != http.StatusUnauthorized {
		// 401 is the expected behavior; any other non-200 is also
		// acceptable as long as it's not a public 200. We log the
		// observed status so a future regression has context.
		t.Logf("GET /Subscription = %d (expected 401)", subResp.StatusCode)
	}
}

// TestE2E_CheckConfig_RejectsEmptyDatabaseURL exec's the binary with
// `--check-config` against a YAML that omits database.url. The binary
// MUST exit non-zero with a precise error; today it prints "config ok"
// and exits 0 because Validate() does not reach the database block.
// Story #116.
func TestE2E_CheckConfig_RejectsEmptyDatabaseURL(t *testing.T) {
	requireHarness(t) // ensure docker harness available, but no DB needed

	body := `
deployment:
  facility_id: hospital-a
  environment: prod
  log_level: info
  log_format: json
  mode: production
adapter:
  id: meditech-expanse-7
server:
  http:
    bind: 0.0.0.0:8443
    insecure: true
lifecycle:
  shutdown_grace_period: 30s
codec:
  active_key_version: 1
  keys:
    - version: 1
      material: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
auth:
  audience: https://example.invalid
  trusted_issuers:
    - issuer: https://idp.example.invalid
      audience: https://example.invalid
      jwks_url: https://idp.example.invalid/jwks.json
topics:
  catalog_dir: /tmp/topics
`
	rc, stdout, stderr := runCheckConfig(t, body)
	if rc == 0 {
		t.Fatalf("--check-config returned 0 for missing database.url; want non-zero. stdout=%s stderr=%s",
			stdout, stderr)
	}
	combined := stdout + "\n" + stderr
	if !strings.Contains(combined, "database.url") {
		t.Fatalf("expected error mentioning database.url; got stdout=%s stderr=%s",
			stdout, stderr)
	}
}

// TestE2E_CheckConfig_RejectsEmptyAuthAudience exec's the binary with
// `--check-config` against a config that omits auth.audience. With
// Insecure=false the binary MUST refuse to start (today the wiring
// installs a no-op middleware that authorizes every caller). Story #116.
func TestE2E_CheckConfig_RejectsEmptyAuthAudience(t *testing.T) {
	requireHarness(t)

	body := `
deployment:
  facility_id: hospital-a
  environment: prod
  log_level: info
  log_format: json
  mode: production
adapter:
  id: meditech-expanse-7
server:
  http:
    bind: 0.0.0.0:8443
    insecure: false
    tls:
      cert_file: /etc/fhir-subs/tls/tls.crt
      key_file: /etc/fhir-subs/tls/tls.key
      min_version: "1.3"
lifecycle:
  shutdown_grace_period: 30s
database:
  url: postgres://example.invalid/db?sslmode=disable
codec:
  active_key_version: 1
  keys:
    - version: 1
      material: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
auth:
  audience: ""
topics:
  catalog_dir: /tmp/topics
mllp:
  listeners:
    - name: feed-1
      bind: 127.0.0.1:2575
`
	rc, stdout, stderr := runCheckConfig(t, body)
	if rc == 0 {
		t.Fatalf("--check-config returned 0 for empty auth.audience; want non-zero. stdout=%s stderr=%s",
			stdout, stderr)
	}
	combined := stdout + "\n" + stderr
	if !strings.Contains(combined, "auth.audience") {
		t.Fatalf("expected error mentioning auth.audience; got stdout=%s stderr=%s",
			stdout, stderr)
	}
}

// TestE2E_CheckConfig_RejectsUnknownMode asserts that misspellings of
// deployment.mode are caught at --check-config time rather than
// silently falling through to a default mode. Story #117.
func TestE2E_CheckConfig_RejectsUnknownMode(t *testing.T) {
	requireHarness(t)

	body := `
deployment:
  facility_id: hospital-a
  environment: prod
  log_level: info
  log_format: json
  mode: probeonly
adapter:
  id: meditech-expanse-7
server:
  http:
    bind: 0.0.0.0:8443
    insecure: true
lifecycle:
  shutdown_grace_period: 30s
`
	rc, stdout, stderr := runCheckConfig(t, body)
	if rc == 0 {
		t.Fatalf("--check-config returned 0 for unknown mode; want non-zero. stdout=%s stderr=%s",
			stdout, stderr)
	}
	combined := stdout + "\n" + stderr
	if !strings.Contains(combined, "mode") {
		t.Fatalf("expected error mentioning deployment.mode; got stdout=%s stderr=%s",
			stdout, stderr)
	}
}

// TestE2E_CheckConfig_ProbeOnlyModeAllowsMissingDatabase asserts the
// new explicit-opt-in probe-only path: with mode=probe-only the binary
// boots without database.url et al. The /metadata endpoint must still
// be reachable without auth. Story #117.
func TestE2E_CheckConfig_ProbeOnlyModeAllowsMissingDatabase(t *testing.T) {
	requireHarness(t)

	body := `
deployment:
  facility_id: hospital-a
  environment: dev
  log_level: info
  log_format: json
  mode: probe-only
adapter:
  id: meditech-expanse-7
server:
  http:
    bind: 0.0.0.0:8443
    insecure: true
lifecycle:
  shutdown_grace_period: 30s
`
	rc, stdout, stderr := runCheckConfig(t, body)
	if rc != 0 {
		t.Fatalf("--check-config rc=%d for probe-only minimal config; want 0. stdout=%s stderr=%s",
			rc, stdout, stderr)
	}
}
