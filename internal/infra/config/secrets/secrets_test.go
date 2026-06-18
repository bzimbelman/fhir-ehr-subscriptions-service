// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package secrets_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/redaction"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/secrets"
)

// TestResolveEnvPlaceholder asserts ${env:VAR} substitution and that the path
// is tagged sensitive.
func TestResolveEnvPlaceholder(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example/db")
	tree := map[string]interface{}{
		"storage": map[string]interface{}{
			"postgres": map[string]interface{}{
				"url": "${env:DATABASE_URL}",
			},
		},
	}
	out, rmap, err := secrets.Resolve(tree, redaction.NewMap())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := out["storage"].(map[string]interface{})["postgres"].(map[string]interface{})["url"]
	if got != "postgres://example/db" {
		t.Fatalf("env not substituted: %v", got)
	}
	if !rmap.IsSensitive("storage.postgres.url") {
		t.Fatalf("path not tagged sensitive: %v", rmap.Paths())
	}
}

// TestResolveFilePlaceholder reads a file's contents and trims trailing
// whitespace per LLD §6.
func TestResolveFilePlaceholder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(p, []byte("super-secret-value\n  \n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	tree := map[string]interface{}{
		"adapter": map[string]interface{}{
			"config": map[string]interface{}{
				"api_key": "${file:" + p + "}",
			},
		},
	}
	out, rmap, err := secrets.Resolve(tree, redaction.NewMap())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	v := out["adapter"].(map[string]interface{})["config"].(map[string]interface{})["api_key"]
	if v != "super-secret-value" {
		t.Fatalf("file value not trimmed: %q", v)
	}
	if !rmap.IsSensitive("adapter.config.api_key") {
		t.Fatalf("path not tagged sensitive")
	}
}

// TestMissingEnvVarRefusesStartup: an unset env var referenced by a placeholder
// is a startup error per LLD §13.
func TestMissingEnvVarRefusesStartup(t *testing.T) {
	// Note: this test mutates process env via Unsetenv, so it must not run in
	// parallel with other tests that read env.
	const name = "DEFINITELY_NOT_SET_FHIR_TEST_XYZ"
	_ = os.Unsetenv(name)
	tree := map[string]interface{}{
		"x": "${env:" + name + "}",
	}
	_, _, err := secrets.Resolve(tree, redaction.NewMap())
	if err == nil {
		t.Fatalf("expected error for unset env var")
	}
	if !strings.Contains(err.Error(), name) {
		t.Fatalf("error must mention the var name; got %v", err)
	}
	if !strings.Contains(err.Error(), "x") {
		t.Fatalf("error must mention the offending path; got %v", err)
	}
}

// TestUnreadableFileRefusesStartup: a missing or unreadable secret file is a
// startup error per LLD §13.
func TestUnreadableFileRefusesStartup(t *testing.T) {
	t.Parallel()
	tree := map[string]interface{}{
		"adapter": map[string]interface{}{
			"config": map[string]interface{}{
				"key_file": "${file:/this/does/not/exist}",
			},
		},
	}
	_, _, err := secrets.Resolve(tree, redaction.NewMap())
	if err == nil {
		t.Fatalf("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "/this/does/not/exist") {
		t.Fatalf("error must mention path; got %v", err)
	}
}

// TestNoPlaceholderUntouched: regular strings are not modified or tagged.
func TestNoPlaceholderUntouched(t *testing.T) {
	t.Parallel()
	tree := map[string]interface{}{
		"deployment": map[string]interface{}{
			"facility_id": "memorial-east",
			"log_level":   "info",
		},
	}
	out, rmap, err := secrets.Resolve(tree, redaction.NewMap())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	dep := out["deployment"].(map[string]interface{})
	if dep["facility_id"] != "memorial-east" {
		t.Fatalf("plain string mutated: %v", dep["facility_id"])
	}
	if rmap.IsSensitive("deployment.facility_id") {
		t.Fatalf("non-placeholder must not tag sensitive")
	}
}

// TestNestedPlaceholders: placeholders deep inside arrays are resolved with
// correct path tagging.
func TestNestedPlaceholders(t *testing.T) {
	t.Setenv("KAFKA_USER", "kafka-user-x")
	t.Setenv("KAFKA_PASSWORD", "kafka-pass-y")
	tree := map[string]interface{}{
		"channels": map[string]interface{}{
			"custom": []interface{}{
				map[string]interface{}{
					"id": "kafka",
					"config": map[string]interface{}{
						"auth": map[string]interface{}{
							"username": "${env:KAFKA_USER}",
							"password": "${env:KAFKA_PASSWORD}",
						},
					},
				},
			},
		},
	}
	out, rmap, err := secrets.Resolve(tree, redaction.NewMap())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	cust := out["channels"].(map[string]interface{})["custom"].([]interface{})
	auth := cust[0].(map[string]interface{})["config"].(map[string]interface{})["auth"].(map[string]interface{})
	if auth["username"] != "kafka-user-x" || auth["password"] != "kafka-pass-y" {
		t.Fatalf("nested env not resolved: %#v", auth)
	}
	if !rmap.IsSensitive("channels.custom.0.config.auth.username") ||
		!rmap.IsSensitive("channels.custom.0.config.auth.password") {
		t.Fatalf("nested paths not tagged: %v", rmap.Paths())
	}
}

// TestPreExistingSensitiveTagsPreserved: paths the validator marked sensitive
// (because the schema says they're sensitive even when not placeholder-wrapped)
// stay tagged after resolution.
func TestPreExistingSensitiveTagsPreserved(t *testing.T) {
	t.Parallel()
	tree := map[string]interface{}{
		"storage": map[string]interface{}{
			"postgres": map[string]interface{}{
				"url": "postgres://operator-baked-in",
			},
		},
	}
	rmap := redaction.NewMap()
	rmap.TagSensitive("storage.postgres.url") // schema-tagged
	_, out, err := secrets.Resolve(tree, rmap)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !out.IsSensitive("storage.postgres.url") {
		t.Fatalf("schema-tagged path lost on resolve")
	}
}
