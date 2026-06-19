// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

// productionBaseYAML is the smallest config that should pass tightened
// Validate() in production mode. Tests below mutate one field at a time
// to assert that each missing required field is rejected with a
// precise, operator-readable error message.
const productionBaseYAML = `
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
database:
  url: postgres://example.invalid/db?sslmode=disable
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
mllp:
  listeners:
    - name: feed-1
      bind: 127.0.0.1:2575
`

// TestValidate_ProductionBaseAccepted is the green-path baseline: the
// canonical example above must pass Validate() once the new strictness
// lands. If this fails after Phase B, the implementation is too strict.
func TestValidate_ProductionBaseAccepted(t *testing.T) {
	t.Parallel()
	p := writeTempYAML(t, productionBaseYAML)
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: production base must pass: %v", err)
	}
	if cfg.Deployment.Mode != "production" {
		t.Fatalf("Deployment.Mode = %q, want production", cfg.Deployment.Mode)
	}
}

// TestValidate_ProductionDefaultMode asserts that an omitted
// deployment.mode defaults to "production" so a typo'd YAML cannot
// silently drop a deployment into probe-only.
func TestValidate_ProductionDefaultMode(t *testing.T) {
	t.Parallel()
	body := strings.Replace(productionBaseYAML, "  mode: production\n", "", 1)
	p := writeTempYAML(t, body)
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Deployment.Mode != "production" {
		t.Fatalf("Deployment.Mode default = %q, want production", cfg.Deployment.Mode)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: default-mode base must pass: %v", err)
	}
}

func TestValidate_RequiresDatabaseURL(t *testing.T) {
	t.Parallel()
	body := strings.Replace(productionBaseYAML,
		"database:\n  url: postgres://example.invalid/db?sslmode=disable\n",
		"", 1)
	p := writeTempYAML(t, body)
	cfg, _ := loadConfig(p)
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error: production mode requires database.url")
	}
	if !strings.Contains(err.Error(), "database.url") {
		t.Fatalf("error must mention database.url: %v", err)
	}
}

func TestValidate_RequiresCodecKeys(t *testing.T) {
	t.Parallel()
	body := strings.Replace(productionBaseYAML,
		"codec:\n  active_key_version: 1\n  keys:\n    - version: 1\n      material: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n",
		"", 1)
	p := writeTempYAML(t, body)
	cfg, _ := loadConfig(p)
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error: production mode requires codec.keys")
	}
	if !strings.Contains(err.Error(), "codec") {
		t.Fatalf("error must mention codec: %v", err)
	}
}

func TestValidate_RequiresCodecActiveKeyVersion(t *testing.T) {
	t.Parallel()
	body := strings.Replace(productionBaseYAML,
		"  active_key_version: 1\n",
		"", 1)
	p := writeTempYAML(t, body)
	cfg, _ := loadConfig(p)
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error: production mode requires codec.active_key_version")
	}
	if !strings.Contains(err.Error(), "active_key_version") {
		t.Fatalf("error must mention codec.active_key_version: %v", err)
	}
}

func TestValidate_RequiresAuthAudienceWhenNotInsecure(t *testing.T) {
	t.Parallel()
	body := strings.Replace(productionBaseYAML,
		"  audience: https://example.invalid\n",
		"  audience: \"\"\n", 1)
	p := writeTempYAML(t, body)
	cfg, _ := loadConfig(p)
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error: production mode requires auth.audience")
	}
	if !strings.Contains(err.Error(), "auth.audience") {
		t.Fatalf("error must mention auth.audience: %v", err)
	}
}

// TestValidate_AllowDevBypassEnablesEmptyAudience proves the explicit
// dev-bypass flag is the ONLY way to start without auth.audience. With
// the flag absent and audience empty, Validate must fail (covered by
// the test above). With the flag set true, audience may be empty.
func TestValidate_AllowDevBypassEnablesEmptyAudience(t *testing.T) {
	t.Parallel()
	body := strings.Replace(productionBaseYAML,
		"  audience: https://example.invalid\n",
		"  audience: \"\"\n  allow_dev_bypass: true\n", 1)
	// Probe-only mode allows missing database; dev-bypass alone does
	// not. Set mode=probe-only too so the rest of Validate is happy.
	body = strings.Replace(body, "  mode: production\n", "  mode: probe-only\n", 1)
	body = strings.Replace(body,
		"database:\n  url: postgres://example.invalid/db?sslmode=disable\n", "", 1)
	body = strings.Replace(body,
		"codec:\n  active_key_version: 1\n  keys:\n    - version: 1\n      material: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n", "", 1)
	body = strings.Replace(body,
		"  trusted_issuers:\n    - issuer: https://idp.example.invalid\n      audience: https://example.invalid\n      jwks_url: https://idp.example.invalid/jwks.json\n", "", 1)
	body = strings.Replace(body, "topics:\n  catalog_dir: /tmp/topics\n", "", 1)
	body = strings.Replace(body, "mllp:\n  listeners:\n    - name: feed-1\n      bind: 127.0.0.1:2575\n", "", 1)
	p := writeTempYAML(t, body)
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if !cfg.Auth.AllowDevBypass {
		t.Fatalf("Auth.AllowDevBypass: parser did not load the flag")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: probe-only + AllowDevBypass must pass: %v", err)
	}
}

func TestValidate_RequiresTrustedIssuersOrClientRegistry(t *testing.T) {
	t.Parallel()
	// Strip trusted_issuers; client_registry isn't set either.
	body := strings.Replace(productionBaseYAML,
		"  trusted_issuers:\n    - issuer: https://idp.example.invalid\n      audience: https://example.invalid\n      jwks_url: https://idp.example.invalid/jwks.json\n",
		"", 1)
	p := writeTempYAML(t, body)
	cfg, _ := loadConfig(p)
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error: production mode requires trusted_issuers or client_registry")
	}
	if !strings.Contains(err.Error(), "trusted_issuers") {
		t.Fatalf("error must mention trusted_issuers: %v", err)
	}
}

// TestValidate_RejectsMLLPListenersWithoutDatabase asserts that
// configuring mllp.listeners without database.url is rejected (a
// listener that cannot persist its messages is dead weight).
func TestValidate_RejectsMLLPListenersWithoutDatabase(t *testing.T) {
	t.Parallel()
	body := strings.Replace(productionBaseYAML,
		"database:\n  url: postgres://example.invalid/db?sslmode=disable\n",
		"", 1)
	p := writeTempYAML(t, body)
	cfg, _ := loadConfig(p)
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error: mllp.listeners with empty database.url")
	}
	// Must mention either mllp.listeners or database.url so the
	// operator can find the offending block.
	if !strings.Contains(err.Error(), "mllp") && !strings.Contains(err.Error(), "database") {
		t.Fatalf("error must mention mllp or database: %v", err)
	}
}

// TestValidate_ProbeOnlyModeDoesNotRequireDatabase asserts the new
// explicit-opt-in path: when deployment.mode=probe-only, the binary
// boots with no DB / codec / auth blocks and serves only the probe
// surface.
func TestValidate_ProbeOnlyModeDoesNotRequireDatabase(t *testing.T) {
	t.Parallel()
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
	p := writeTempYAML(t, body)
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Deployment.Mode != "probe-only" {
		t.Fatalf("Deployment.Mode = %q, want probe-only", cfg.Deployment.Mode)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: probe-only minimal config must pass: %v", err)
	}
}

// TestValidate_RejectsUnknownMode asserts that misspelling the mode
// (e.g. `production-mode` or `probe`) fails Validate rather than
// silently falling through to one of the supported modes.
func TestValidate_RejectsUnknownMode(t *testing.T) {
	t.Parallel()
	body := strings.Replace(productionBaseYAML,
		"  mode: production\n",
		"  mode: probeonly\n", 1)
	p := writeTempYAML(t, body)
	cfg, _ := loadConfig(p)
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error: unknown deployment.mode")
	}
	if !strings.Contains(err.Error(), "mode") {
		t.Fatalf("error must mention deployment.mode: %v", err)
	}
}
