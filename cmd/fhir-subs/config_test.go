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

// TestApplySets_AuthKnobs covers OP #167: every auth.* knob the operator
// might reach for during an incident MUST be settable via --set so the
// rollout doesn't require a YAML edit + restart. The list is the union
// of supplements #110 and #126 — fields the applySets allowlist
// previously omitted.
//
// Duration fields are parsed with time.ParseDuration; lists are
// JSON-decoded so an operator who needs to shorten a JWKS allowlist can
// pass `--set auth.jwks_allowed_hosts=["a.example","b.example"]`
// without touching a struct list shape via dotted keys.
func TestApplySets_AuthKnobs(t *testing.T) {
	t.Parallel()

	type check struct {
		set  string
		want func(*Config) bool
		why  string
	}
	cases := []check{
		{
			set:  "auth.access_token_ttl=15m",
			want: func(c *Config) bool { return c.Auth.AccessTokenTTL == 15*time.Minute },
			why:  "AccessTokenTTL = 15m",
		},
		{
			set:  "auth.jwks_cache_ttl=2h30m",
			want: func(c *Config) bool { return c.Auth.JWKSCacheTTL == 2*time.Hour+30*time.Minute },
			why:  "JWKSCacheTTL = 2h30m",
		},
		{
			set:  "auth.clock_skew=30s",
			want: func(c *Config) bool { return c.Auth.ClockSkew == 30*time.Second },
			why:  "ClockSkew = 30s",
		},
		{
			set: `auth.jwks_allowed_hosts=["idp.example.com","backup-idp.example.com"]`,
			want: func(c *Config) bool {
				return len(c.Auth.JWKSAllowed) == 2 &&
					c.Auth.JWKSAllowed[0] == "idp.example.com" &&
					c.Auth.JWKSAllowed[1] == "backup-idp.example.com"
			},
			why: "JWKSAllowed = 2 hosts",
		},
		{
			set: `auth.trusted_issuers=[{"issuer":"https://idp","audience":"sub","jwks_url":"https://idp/jwks"}]`,
			want: func(c *Config) bool {
				return len(c.Auth.TrustedIssuers) == 1 &&
					c.Auth.TrustedIssuers[0].Issuer == "https://idp" &&
					c.Auth.TrustedIssuers[0].Audience == "sub" &&
					c.Auth.TrustedIssuers[0].JWKSURL == "https://idp/jwks"
			},
			why: "TrustedIssuers = 1 entry",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.why, func(t *testing.T) {
			t.Parallel()
			p := writeTempYAML(t, minimalValidYAML)
			cfg, err := loadConfig(p)
			if err != nil {
				t.Fatalf("loadConfig: %v", err)
			}
			if err := applySets(cfg, []string{tc.set}); err != nil {
				t.Fatalf("applySets %q: %v", tc.set, err)
			}
			if !tc.want(cfg) {
				t.Fatalf("--set %q did not produce expected state (%s); cfg.Auth=%+v",
					tc.set, tc.why, cfg.Auth)
			}
		})
	}
}

// TestApplySets_AuthKnobs_BadDurationRedacts asserts the redacting
// error path covers the new duration knobs. A misparsed duration must
// not echo the operator's value because they may have appended a
// secret-looking suffix.
func TestApplySets_AuthKnobs_BadDurationRedacts(t *testing.T) {
	t.Parallel()
	p := writeTempYAML(t, minimalValidYAML)
	cfg, _ := loadConfig(p)
	for _, key := range []string{
		"auth.access_token_ttl",
		"auth.jwks_cache_ttl",
		"auth.clock_skew",
	} {
		key := key
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			err := applySets(cfg, []string{key + "=hunter2"})
			if err == nil {
				t.Fatalf("expected error for --set %s=hunter2", key)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Fatalf("error must not echo raw value for %s: %v", key, err)
			}
			if !strings.Contains(err.Error(), "<redacted>") {
				t.Fatalf("error should indicate redaction for %s: %v", key, err)
			}
		})
	}
}

// TestApplySets_AuthKnobs_BadJSONRedacts asserts list-typed knobs
// reject malformed JSON without leaking the raw value.
func TestApplySets_AuthKnobs_BadJSONRedacts(t *testing.T) {
	t.Parallel()
	p := writeTempYAML(t, minimalValidYAML)
	cfg, _ := loadConfig(p)
	for _, key := range []string{
		"auth.jwks_allowed_hosts",
		"auth.trusted_issuers",
	} {
		key := key
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			err := applySets(cfg, []string{key + "=hunter2-bad-json"})
			if err == nil {
				t.Fatalf("expected error for --set %s=hunter2-bad-json", key)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Fatalf("error must not echo raw value for %s: %v", key, err)
			}
			if !strings.Contains(err.Error(), "<redacted>") {
				t.Fatalf("error should indicate redaction for %s: %v", key, err)
			}
		})
	}
}

// TestApplySets_RedactsValueOnError asserts that when --set fails parsing
// (bad bool, bad duration, bad int) the error MUST NOT echo the raw RHS
// because the value can carry a secret (e.g. `--set
// channels.email.smtp_password=hunter2`). A future stricter loader will
// support every dotted key; until then the failure path here is the
// place an operator's secret is most likely to reach a log via stderr.
// (S-1.1)
func TestApplySets_RedactsValueOnError(t *testing.T) {
	t.Parallel()

	p := writeTempYAML(t, minimalValidYAML)
	cfg, _ := loadConfig(p)

	cases := []struct {
		name string
		set  string
		bad  string
	}{
		{name: "bad bool", set: "server.http.insecure=hunter2", bad: "hunter2"},
		{name: "bad duration", set: "lifecycle.shutdown_grace_period=hunter2", bad: "hunter2"},
		{name: "bad int", set: "server.http.max_header_bytes=hunter2", bad: "hunter2"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := applySets(cfg, []string{tc.set})
			if err == nil {
				t.Fatalf("expected error for %q", tc.set)
			}
			if strings.Contains(err.Error(), tc.bad) {
				t.Fatalf("error must not echo raw value %q: %v", tc.bad, err)
			}
			if !strings.Contains(err.Error(), "<redacted>") {
				t.Fatalf("error should indicate redaction: %v", err)
			}
		})
	}
}
