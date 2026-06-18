// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const minimalValidYAML = `
deployment:
  facility_id: hospital-a
  environment: dev
  log_level: info
  log_format: json
adapter:
  id: meditech-expanse-7
  version_pin: ">=1.0.0"
server:
  http:
    bind: 0.0.0.0:8443
    insecure: true
lifecycle:
  shutdown_grace_period: 30s
`

func writeTempYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	return p
}

func TestLoadConfig_Minimal(t *testing.T) {
	t.Parallel()

	p := writeTempYAML(t, minimalValidYAML)
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Deployment.FacilityID != "hospital-a" {
		t.Fatalf("facility_id: %q", cfg.Deployment.FacilityID)
	}
	if cfg.Deployment.Environment != "dev" {
		t.Fatalf("environment: %q", cfg.Deployment.Environment)
	}
	if cfg.Deployment.LogLevel != "info" {
		t.Fatalf("log_level: %q", cfg.Deployment.LogLevel)
	}
	if cfg.Deployment.LogFormat != "json" {
		t.Fatalf("log_format: %q", cfg.Deployment.LogFormat)
	}
	if cfg.Adapter.ID != "meditech-expanse-7" {
		t.Fatalf("adapter.id: %q", cfg.Adapter.ID)
	}
	if cfg.Adapter.VersionPin != ">=1.0.0" {
		t.Fatalf("adapter.version_pin: %q", cfg.Adapter.VersionPin)
	}
	if cfg.Server.HTTP.Bind != "0.0.0.0:8443" {
		t.Fatalf("server.http.bind: %q", cfg.Server.HTTP.Bind)
	}
	if !cfg.Server.HTTP.Insecure {
		t.Fatalf("server.http.insecure: want true")
	}
	if cfg.Lifecycle.ShutdownGracePeriod != 30*time.Second {
		t.Fatalf("shutdown_grace_period: %v", cfg.Lifecycle.ShutdownGracePeriod)
	}
}

func TestLoadConfig_MissingFacilityID(t *testing.T) {
	t.Parallel()

	body := `
deployment:
  environment: dev
adapter:
  id: meditech-expanse-7
server:
  http:
    bind: 0.0.0.0:8443
    insecure: true
`
	p := writeTempYAML(t, body)
	cfg, err := loadConfig(p)
	if err != nil {
		// loadConfig itself is allowed to be lenient about required fields;
		// validation runs separately. Continue.
		_ = cfg
	}
	verr := cfg.Validate()
	if verr == nil {
		t.Fatal("expected validation error for missing facility_id")
	}
	if !strings.Contains(verr.Error(), "facility_id") {
		t.Fatalf("error should mention facility_id: %v", verr)
	}
}

func TestLoadConfig_MissingAdapterID(t *testing.T) {
	t.Parallel()

	body := `
deployment:
  facility_id: hospital-a
server:
  http:
    bind: 0.0.0.0:8443
    insecure: true
`
	p := writeTempYAML(t, body)
	cfg, _ := loadConfig(p)
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for missing adapter.id")
	} else if !strings.Contains(err.Error(), "adapter.id") {
		t.Fatalf("error should mention adapter.id: %v", err)
	}
}

func TestLoadConfig_TLSRequiredWhenNotInsecure(t *testing.T) {
	t.Parallel()

	body := `
deployment:
  facility_id: hospital-a
adapter:
  id: meditech-expanse-7
server:
  http:
    bind: 0.0.0.0:8443
    insecure: false
`
	p := writeTempYAML(t, body)
	cfg, _ := loadConfig(p)
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error: insecure=false without tls.cert_file")
	}
	if !strings.Contains(err.Error(), "tls") {
		t.Fatalf("error should mention tls: %v", err)
	}
}

func TestLoadConfig_FileNotFound(t *testing.T) {
	t.Parallel()

	_, err := loadConfig("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	t.Parallel()

	p := writeTempYAML(t, "::: not yaml :::")
	_, err := loadConfig(p)
	if err == nil {
		t.Fatal("expected error for invalid yaml")
	}
}

func TestLoadConfig_DefaultGracePeriod(t *testing.T) {
	t.Parallel()

	body := `
deployment:
  facility_id: hospital-a
adapter:
  id: meditech-expanse-7
server:
  http:
    bind: 0.0.0.0:8443
    insecure: true
`
	p := writeTempYAML(t, body)
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Lifecycle.ShutdownGracePeriod != 30*time.Second {
		t.Fatalf("default grace period: %v", cfg.Lifecycle.ShutdownGracePeriod)
	}
}

func TestLoadConfig_ApplySetsOverride(t *testing.T) {
	t.Parallel()

	p := writeTempYAML(t, minimalValidYAML)
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if err := applySets(cfg, []string{"deployment.log_level=debug"}); err != nil {
		t.Fatalf("applySets: %v", err)
	}
	if cfg.Deployment.LogLevel != "debug" {
		t.Fatalf("expected log_level=debug after --set, got %q", cfg.Deployment.LogLevel)
	}
}

func TestLoadConfig_ApplySetsBadFormat(t *testing.T) {
	t.Parallel()

	p := writeTempYAML(t, minimalValidYAML)
	cfg, _ := loadConfig(p)
	if err := applySets(cfg, []string{"no-equals-sign"}); err == nil {
		t.Fatal("expected error for malformed --set")
	}
}
