// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/redaction"
)

// TestIntegrationLoadsArchitectureExampleYAML walks the entire boot path on
// the canonical example YAML from docs/architecture.md.
//
// Setup: every ${env:VAR} placeholder in the YAML is satisfied by
// t.Setenv. Verifies:
//   - Module.Start succeeds.
//   - The published snapshot reflects parsed values.
//   - Every placeholder-resolved path is tagged sensitive.
//   - Serialization through redaction substitutes [redacted] for sensitives.
func TestIntegrationLoadsArchitectureExampleYAML(t *testing.T) {
	// All env placeholders the example references.
	envs := map[string]string{
		"DATABASE_URL":                "postgres://example.org/db",
		"STORAGE_ENCRYPTION_KEY":      "01234567890abcdef01234567890abcdef",
		"EPIC_CLIENT_ID":              "epic-client-id-x",
		"EPIC_INTERCONNECT_KEY":       "epic-interconnect-key-x",
		"SMTP_USERNAME":               "smtp-user",
		"SMTP_PASSWORD":               "smtp-pass",
		"KAFKA_USER":                  "kafka-user",
		"KAFKA_PASSWORD":              "kafka-pass",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "https://otel.example/v1/traces",
	}
	for k, v := range envs {
		t.Setenv(k, v)
	}

	// Copy the canonical YAML into a temp file (the test is isolated from any
	// real /etc/fhir-subs/config.yaml).
	src, readErr := os.ReadFile("testdata/architecture_example.yaml")
	if readErr != nil {
		t.Fatalf("read fixture: %v", readErr)
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if writeErr := os.WriteFile(cfgPath, src, 0o600); writeErr != nil {
		t.Fatalf("write tmp config: %v", writeErr)
	}

	// Boot the module.
	ctx := context.Background()
	mod, h, startErr := config.Start(ctx, config.CliArgs{
		ConfigPath: cfgPath,
	}, config.Context{
		Clock:  time.Now,
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	if startErr != nil {
		t.Fatalf("Start: %v", startErr)
	}
	defer func() {
		if shutdownErr := mod.Shutdown(ctx); shutdownErr != nil {
			t.Fatalf("Shutdown: %v", shutdownErr)
		}
	}()

	eff := h.Read()

	// Spot-check: typed views populated.
	if eff.Deployment.FacilityID != "memorial-hospital-east" {
		t.Fatalf("facility_id: %q", eff.Deployment.FacilityID)
	}
	if eff.Server.HTTP.Bind != "0.0.0.0:8443" {
		t.Fatalf("server.http.bind: %q", eff.Server.HTTP.Bind)
	}

	// Resolved secret values present in the snapshot.
	pgURL, _ := nestedString(eff.Tree, "storage", "postgres", "url")
	if pgURL != "postgres://example.org/db" {
		t.Fatalf("storage.postgres.url not resolved: %q", pgURL)
	}

	// Sensitive paths are tagged.
	if !eff.Redaction.IsSensitive("storage.postgres.url") {
		t.Fatalf("storage.postgres.url not tagged sensitive: %v", eff.Redaction.Paths())
	}
	if !eff.Redaction.IsSensitive("adapter.config.fhir_auth.client_id") {
		t.Fatalf("adapter client_id not tagged sensitive: %v", eff.Redaction.Paths())
	}

	// Redacted serialization: the secret value never appears.
	redacted := eff.Redaction.Redact(eff.Tree, "")
	js, err := json.Marshal(redacted)
	if err != nil {
		t.Fatalf("marshal redacted: %v", err)
	}
	for _, secret := range []string{
		"postgres://example.org/db",
		"01234567890abcdef01234567890abcdef",
		"epic-client-id-x",
		"epic-interconnect-key-x",
		"smtp-pass",
		"kafka-pass",
	} {
		if strings.Contains(string(js), secret) {
			t.Fatalf("secret %q leaked into redacted serialization: %s", secret, string(js))
		}
	}
	if !strings.Contains(string(js), redaction.Redacted) {
		t.Fatalf("redacted sentinel missing from serialization")
	}
}

// TestIntegrationCheckOnly: --check-config runs validate-and-exit; no snapshot
// is published when CheckOnly is set, but Start returns success.
func TestIntegrationCheckOnly(t *testing.T) {
	envs := map[string]string{
		"DATABASE_URL":                "postgres://x",
		"STORAGE_ENCRYPTION_KEY":      "k",
		"EPIC_CLIENT_ID":              "x",
		"EPIC_INTERCONNECT_KEY":       "x",
		"SMTP_USERNAME":               "x",
		"SMTP_PASSWORD":               "x",
		"KAFKA_USER":                  "x",
		"KAFKA_PASSWORD":              "x",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "x",
	}
	for k, v := range envs {
		t.Setenv(k, v)
	}
	src, _ := os.ReadFile("testdata/architecture_example.yaml")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, src, 0o600); err != nil {
		t.Fatalf("write tmp config: %v", err)
	}
	ctx := context.Background()
	_, _, err := config.Start(ctx, config.CliArgs{
		ConfigPath: cfgPath,
		CheckOnly:  true,
	}, config.Context{Clock: time.Now})
	if err != nil {
		t.Fatalf("--check-config should pass on canonical YAML: %v", err)
	}
}

// TestIntegrationRejectMissingFacilityID: drops a hard-required field and
// confirms Start returns a structured error pointing at the offending path.
func TestIntegrationRejectMissingFacilityID(t *testing.T) {
	envs := map[string]string{
		"DATABASE_URL":                "postgres://x",
		"STORAGE_ENCRYPTION_KEY":      "k",
		"EPIC_CLIENT_ID":              "x",
		"EPIC_INTERCONNECT_KEY":       "x",
		"SMTP_USERNAME":               "x",
		"SMTP_PASSWORD":               "x",
		"KAFKA_USER":                  "x",
		"KAFKA_PASSWORD":              "x",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "x",
	}
	for k, v := range envs {
		t.Setenv(k, v)
	}
	yamlSrc, _ := os.ReadFile("testdata/architecture_example.yaml")
	bad := strings.ReplaceAll(string(yamlSrc),
		`facility_id: "memorial-hospital-east"`, `# facility_id removed`)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(bad), 0o600); err != nil {
		t.Fatalf("write bad config: %v", err)
	}
	ctx := context.Background()
	_, _, err := config.Start(ctx, config.CliArgs{ConfigPath: cfgPath},
		config.Context{Clock: time.Now})
	if err == nil {
		t.Fatalf("expected start failure on missing facility_id")
	}
	if !strings.Contains(err.Error(), "facility_id") {
		t.Fatalf("error must mention facility_id; got %v", err)
	}
}

func nestedString(tree map[string]interface{}, path ...string) (string, bool) {
	cur := tree
	for i, k := range path {
		v, present := cur[k]
		if !present {
			return "", false
		}
		if i == len(path)-1 {
			s, isString := v.(string)
			return s, isString
		}
		m, isMap := v.(map[string]interface{})
		if !isMap {
			return "", false
		}
		cur = m
	}
	return "", false
}
