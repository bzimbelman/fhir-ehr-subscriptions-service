// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
	"time"
)

// TestLoadConfig_B4_FullWiring_Fields asserts that the config loader
// understands the database / codec / auth / mllp / pipeline / channels
// blocks the production wiring depends on. Before B-4 these blocks lived
// only in `Extra` and run.go ignored them.
//
// B-4.
func TestLoadConfig_B4_FullWiring_Fields(t *testing.T) {
	t.Parallel()

	yaml := `
deployment:
  facility_id: hospital-a
  environment: dev
adapter:
  id: default
server:
  http:
    bind: 0.0.0.0:8443
    insecure: true
lifecycle:
  shutdown_grace_period: 30s
database:
  url: postgres://test@localhost:5432/db
codec:
  active_key_version: 1
  keys:
    - version: 1
      material: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
auth:
  audience: https://api.example.com
  token_url: https://api.example.com/token
  issued_secret: c2VjcmV0LWtleS1iYXNlNjQ=
  issued_issuer: https://api.example.com
  access_token_ttl: 5m
  jwks_cache_ttl: 1h
  clock_skew: 60s
  trusted_issuers:
    - issuer: https://idp.example.com
      audience: https://api.example.com
      jwks_url: https://idp.example.com/.well-known/jwks.json
mllp:
  listeners:
    - name: adt-feed
      bind: 0.0.0.0:2575
  max_message_bytes: 1048576
  persist_timeout: 5s
  max_connections: 200
  max_connections_per_ip: 10
  shutdown_drain_grace: 10s
pipeline:
  hl7_processor:
    claim_batch_size: 32
    idle_poll_interval: 200ms
  matcher:
    claim_batch_size: 32
    idle_poll_interval: 200ms
  submatcher:
    claim_batch_size: 32
    idle_poll_interval: 200ms
  scheduler:
    claim_batch_size: 32
    idle_poll_interval: 200ms
  correlation_hold_window: 30s
channels:
  rest_hook:
    user_agent: fhir-subs/1.0
    request_timeout: 30s
  websocket:
    origin_patterns:
      - "*.example.com"
    idle_timeout: 5m
    ping_interval: 30s
`
	p := writeTempYAML(t, yaml)
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Database.URL != "postgres://test@localhost:5432/db" {
		t.Errorf("database.url: %q", cfg.Database.URL)
	}
	if cfg.Codec.ActiveKeyVersion != 1 {
		t.Errorf("codec.active_key_version: %d", cfg.Codec.ActiveKeyVersion)
	}
	if len(cfg.Codec.Keys) != 1 || cfg.Codec.Keys[0].Version != 1 {
		t.Errorf("codec.keys: %+v", cfg.Codec.Keys)
	}
	if cfg.Auth.Audience != "https://api.example.com" {
		t.Errorf("auth.audience: %q", cfg.Auth.Audience)
	}
	if cfg.Auth.AccessTokenTTL != 5*time.Minute {
		t.Errorf("auth.access_token_ttl: %v", cfg.Auth.AccessTokenTTL)
	}
	if len(cfg.Auth.TrustedIssuers) != 1 ||
		cfg.Auth.TrustedIssuers[0].Issuer != "https://idp.example.com" {
		t.Errorf("auth.trusted_issuers: %+v", cfg.Auth.TrustedIssuers)
	}
	if len(cfg.MLLP.Listeners) != 1 || cfg.MLLP.Listeners[0].Name != "adt-feed" {
		t.Errorf("mllp.listeners: %+v", cfg.MLLP.Listeners)
	}
	if cfg.MLLP.MaxConnections != 200 {
		t.Errorf("mllp.max_connections: %d", cfg.MLLP.MaxConnections)
	}
	if cfg.Pipeline.HL7Processor.ClaimBatchSize != 32 {
		t.Errorf("pipeline.hl7_processor.claim_batch_size: %d",
			cfg.Pipeline.HL7Processor.ClaimBatchSize)
	}
	if cfg.Pipeline.CorrelationHoldWindow != 30*time.Second {
		t.Errorf("pipeline.correlation_hold_window: %v",
			cfg.Pipeline.CorrelationHoldWindow)
	}
	if cfg.Channels.RestHook.UserAgent != "fhir-subs/1.0" {
		t.Errorf("channels.rest_hook.user_agent: %q",
			cfg.Channels.RestHook.UserAgent)
	}
	if len(cfg.Channels.WebSocket.OriginPatterns) != 1 {
		t.Errorf("channels.websocket.origin_patterns: %v",
			cfg.Channels.WebSocket.OriginPatterns)
	}
}

// TestApplySets_B4_NewKeys exercises every new --set key the loader
// supports for the B-4 wiring. The loader's existing applySets set was
// explicit; missing entries silently no-op'd, which let typo'd keys hit
// production.
//
// B-4.
func TestApplySets_B4_NewKeys(t *testing.T) {
	t.Parallel()

	cfg := defaultConfig()
	err := applySets(cfg, []string{
		"database.url=postgres://localhost:5432/db",
		"auth.audience=https://api.example",
		"auth.token_url=https://api.example/token",
		"auth.issued_issuer=https://api.example",
		"auth.issued_secret=Zm9v",
		"auth.allow_insecure_jwks=true",
		"codec.active_key_version=2",
	})
	if err != nil {
		t.Fatalf("applySets: %v", err)
	}
	if cfg.Database.URL != "postgres://localhost:5432/db" {
		t.Errorf("database.url: %q", cfg.Database.URL)
	}
	if cfg.Auth.Audience != "https://api.example" {
		t.Errorf("auth.audience: %q", cfg.Auth.Audience)
	}
	if cfg.Auth.IssuedSecret != "Zm9v" {
		t.Errorf("auth.issued_secret: %q", cfg.Auth.IssuedSecret)
	}
	if !cfg.Auth.AllowInsecure {
		t.Errorf("auth.allow_insecure_jwks: want true")
	}
	if cfg.Codec.ActiveKeyVersion != 2 {
		t.Errorf("codec.active_key_version: %d", cfg.Codec.ActiveKeyVersion)
	}
}

// TestApplySets_B4_UnknownKeyRejected makes sure an unsupported key is
// still rejected; the new keys must not accidentally widen the loader's
// surface for arbitrary --set arguments.
//
// B-4.
func TestApplySets_B4_UnknownKeyRejected(t *testing.T) {
	t.Parallel()

	cfg := defaultConfig()
	err := applySets(cfg, []string{"database.urll=oops"})
	if err == nil {
		t.Fatalf("applySets: want error for typo'd key")
	}
	if !strings.Contains(err.Error(), "database.urll") {
		t.Errorf("err = %q, want it to mention the bad key", err.Error())
	}
}
