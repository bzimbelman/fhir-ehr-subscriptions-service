// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Story #119: ${env:VAR} and ${file:/path} placeholder interpolation must
// run on the raw config bytes before YAML decode. These tests drive the
// real loader against real env vars and real files in t.TempDir().

const interpYAMLEnv = `deployment:
  facility_id: hospital-a
  environment: dev
  log_level: info
  log_format: json
adapter:
  id: meditech-expanse-7
server:
  http:
    bind: 0.0.0.0:8443
    insecure: true
lifecycle:
  shutdown_grace_period: 30s
database:
  url: ${env:STORY_119_DB_URL}
`

func TestLoadConfig_EnvPlaceholder_Substituted(t *testing.T) {
	t.Setenv("STORY_119_DB_URL", "postgres://app:secret@db/fhir?sslmode=disable")
	p := writeTempYAML(t, interpYAMLEnv)

	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	got := cfg.Database.URL
	want := "postgres://app:secret@db/fhir?sslmode=disable"
	if got != want {
		t.Fatalf("database.url after interpolation: got %q want %q", got, want)
	}
}

func TestLoadConfig_EnvPlaceholder_MissingFailsLoud(t *testing.T) {
	if _, ok := os.LookupEnv("STORY_119_MISSING"); ok {
		_ = os.Unsetenv("STORY_119_MISSING")
	}
	body := strings.Replace(interpYAMLEnv, "${env:STORY_119_DB_URL}", "${env:STORY_119_MISSING}", 1)
	p := writeTempYAML(t, body)

	_, err := loadConfig(p)
	if err == nil {
		t.Fatalf("loadConfig: want error for unset env, got nil")
	}
	if !strings.Contains(err.Error(), "STORY_119_MISSING") {
		t.Fatalf("error must name the missing env var; got %v", err)
	}
	if !strings.Contains(err.Error(), "env") {
		t.Fatalf("error must mention env; got %v", err)
	}
}

func TestLoadConfig_FilePlaceholder_Substituted(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "db_url")
	// Real file. Include a trailing newline — the loader must trim it
	// because operators using `printf '%s\n' ...` is normal.
	const secretContent = "postgres://app:secret@db/fhir?sslmode=disable\n"
	if err := os.WriteFile(secretPath, []byte(secretContent), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	body := strings.Replace(interpYAMLEnv,
		"${env:STORY_119_DB_URL}",
		"${file:"+secretPath+"}",
		1,
	)
	p := writeTempYAML(t, body)

	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	got := cfg.Database.URL
	want := strings.TrimRight(secretContent, "\n")
	if got != want {
		t.Fatalf("database.url after file interpolation: got %q want %q", got, want)
	}
}

func TestLoadConfig_FilePlaceholder_MissingFailsLoud(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist")
	body := strings.Replace(interpYAMLEnv,
		"${env:STORY_119_DB_URL}",
		"${file:"+missing+"}",
		1,
	)
	p := writeTempYAML(t, body)

	_, err := loadConfig(p)
	if err == nil {
		t.Fatalf("loadConfig: want error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("error must name the missing file path; got %v", err)
	}
	if !strings.Contains(err.Error(), "file") {
		t.Fatalf("error must mention file; got %v", err)
	}
}

// Interpolation must work on a nested key — codec.keys[0].material is
// the canonical operator-supplied secret.
func TestLoadConfig_EnvPlaceholder_NestedKey(t *testing.T) {
	const keyMat = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	t.Setenv("STORY_119_KEY_V1", keyMat)

	body := `deployment:
  facility_id: hospital-a
  environment: dev
  log_level: info
  log_format: json
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
      material: ${env:STORY_119_KEY_V1}
`
	p := writeTempYAML(t, body)
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.Codec.Keys) != 1 {
		t.Fatalf("codec.keys: got %d want 1", len(cfg.Codec.Keys))
	}
	if cfg.Codec.Keys[0].Material != keyMat {
		t.Fatalf("codec.keys[0].material: got %q want %q", cfg.Codec.Keys[0].Material, keyMat)
	}
}

// Interpolation in the middle of a longer string must work — a DSN
// where only the password segment is the secret.
func TestLoadConfig_EnvPlaceholder_PartOfLongerString(t *testing.T) {
	t.Setenv("STORY_119_DB_PASS", "p@ss!w0rd")

	body := `deployment:
  facility_id: hospital-a
  environment: dev
  log_level: info
  log_format: json
adapter:
  id: meditech-expanse-7
server:
  http:
    bind: 0.0.0.0:8443
    insecure: true
lifecycle:
  shutdown_grace_period: 30s
database:
  url: "postgres://app:${env:STORY_119_DB_PASS}@db:5432/fhir"
`
	p := writeTempYAML(t, body)
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	want := "postgres://app:p@ss!w0rd@db:5432/fhir"
	if cfg.Database.URL != want {
		t.Fatalf("database.url: got %q want %q", cfg.Database.URL, want)
	}
}

// A literal `${env:` or `${file:` that survived (operator typo, e.g.
// `$(env:FOO)` or `${ENV:FOO}` with wrong case, or `${env:}` empty) must
// be rejected. The loader's interpolator runs the canonical syntax; any
// other variant would slip past and validate-time must catch it.
//
// Today we test the empty-name case for env: ${env:} should fail at
// interpolation time (missing variable name).
func TestLoadConfig_EnvPlaceholder_EmptyNameFailsLoud(t *testing.T) {
	body := strings.Replace(interpYAMLEnv, "${env:STORY_119_DB_URL}", "${env:}", 1)
	p := writeTempYAML(t, body)

	_, err := loadConfig(p)
	if err == nil {
		t.Fatalf("loadConfig: want error for empty env name, got nil")
	}
}

// Validate() must reject a literal placeholder that survived the
// interpolation pass — e.g. an operator wrote `$ENV{FOO}` thinking
// shell-style works. Surviving literal proves a typo went unnoticed.
func TestValidate_RejectsSurvivingLiteralPlaceholder(t *testing.T) {
	cfg := defaultConfig()
	cfg.Deployment.FacilityID = "hospital-a"
	cfg.Adapter.ID = "meditech-expanse-7"
	cfg.Server.HTTP.Insecure = true
	cfg.Auth.AllowDevBypass = true
	// Embed a bogus literal placeholder that an operator typo could
	// produce. Validate must catch it rather than silently accept.
	cfg.Database.URL = "postgres://${env:DB_PASS_TYPOD"

	err := cfg.Validate()
	if err == nil {
		t.Fatalf("Validate: want error for surviving placeholder, got nil")
	}
	if !strings.Contains(err.Error(), "${") {
		t.Fatalf("Validate error must call out the literal `${`; got %v", err)
	}
}
