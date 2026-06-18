// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package loader_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/config/loader"
)

// TestParseCLIFlags asserts that --set dotted.key=value entries become a sparse
// nested map. The loader exposes a CLI parser; the merger consumes its output.
func TestParseCLIFlags(t *testing.T) {
	t.Parallel()
	args := loader.CLIArgs{
		LogLevel: "debug",
		Sets: []string{
			"deployment.facility_id=memorial-east",
			"server.http.bind=0.0.0.0:9000",
			"delivery.retry.max_attempts=4",
		},
	}
	got, err := loader.ParseCLI(args)
	if err != nil {
		t.Fatalf("ParseCLI: %v", err)
	}
	want := map[string]interface{}{
		"deployment": map[string]interface{}{
			"facility_id": "memorial-east",
			"log_level":   "debug",
		},
		"server": map[string]interface{}{
			"http": map[string]interface{}{
				"bind": "0.0.0.0:9000",
			},
		},
		"delivery": map[string]interface{}{
			"retry": map[string]interface{}{
				"max_attempts": "4",
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseCLI mismatch:\n got=%#v\nwant=%#v", got, want)
	}
}

func TestParseCLIRejectsBadSet(t *testing.T) {
	t.Parallel()
	cases := []string{
		"foo",                // no =
		"=value",             // empty key
		"deployment.=value",  // empty leaf segment
		".facility_id=value", // leading dot
	}
	for _, s := range cases {
		s := s
		t.Run(s, func(t *testing.T) {
			_, err := loader.ParseCLI(loader.CLIArgs{Sets: []string{s}})
			if err == nil {
				t.Fatalf("expected error for %q", s)
			}
		})
	}
}

// TestReadEnvForKnownKeys asserts that the loader walks a known-key table
// (provided by the schemas module at startup) and reads only those env vars,
// silently ignoring unknown env vars. The known list is path-keyed so
// multi-word config keys like "trusted_issuers" survive the round-trip.
// Array indices are positional.
func TestReadEnvForKnownKeys(t *testing.T) {
	t.Setenv("STORAGE_POSTGRES_URL", "postgres://example")
	t.Setenv("AUTH_TRUSTED_ISSUERS_0_ISSUER", "https://idp.example.org")
	t.Setenv("RANDOM_UNKNOWN_VAR", "should be ignored")

	known := []string{
		"storage.postgres.url",
		"auth.trusted_issuers.0.issuer",
		"auth.trusted_issuers.0.jwks_url", // unset, must not appear
	}
	got := loader.ReadEnvForKnownKeys(known)
	want := map[string]interface{}{
		"storage": map[string]interface{}{
			"postgres": map[string]interface{}{
				"url": "postgres://example",
			},
		},
		"auth": map[string]interface{}{
			"trusted_issuers": []interface{}{
				map[string]interface{}{
					"issuer": "https://idp.example.org",
				},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadEnvForKnownKeys mismatch:\n got=%#v\nwant=%#v", got, want)
	}
}

// TestReadFileYAML asserts a small YAML config parses into a generic tree.
func TestReadFileYAML(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	yaml := `
deployment:
  facility_id: memorial-east
  log_level: info
server:
  http:
    bind: "0.0.0.0:8443"
`
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	got, err := loader.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	dep, ok := got["deployment"].(map[string]interface{})
	if !ok || dep["facility_id"] != "memorial-east" || dep["log_level"] != "info" {
		t.Fatalf("yaml deployment mismatch: %#v", got["deployment"])
	}
	srv, ok := got["server"].(map[string]interface{})
	if !ok {
		t.Fatalf("server missing: %#v", got)
	}
	http, ok := srv["http"].(map[string]interface{})
	if !ok || http["bind"] != "0.0.0.0:8443" {
		t.Fatalf("server.http mismatch: %#v", srv["http"])
	}
}

// TestReadFileTOML asserts a TOML config (.toml extension) parses too.
func TestReadFileTOML(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	toml := `
[deployment]
facility_id = "memorial-east"
log_level = "info"

[server.http]
bind = "0.0.0.0:8443"
`
	if err := os.WriteFile(p, []byte(toml), 0o600); err != nil {
		t.Fatalf("write toml: %v", err)
	}
	got, err := loader.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	dep, ok := got["deployment"].(map[string]interface{})
	if !ok || dep["facility_id"] != "memorial-east" {
		t.Fatalf("toml deployment mismatch: %#v", got["deployment"])
	}
}

// TestReadFileMissingIsEmpty: a missing file is returned as an empty tree (the
// merger handles "config file not provided"). An unreadable but present file
// is an error.
func TestReadFileMissingIsEmpty(t *testing.T) {
	t.Parallel()
	got, err := loader.ReadFile("")
	if err != nil {
		t.Fatalf("ReadFile empty path: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty tree, got %#v", got)
	}
	got, err = loader.ReadFile("/this/path/does/not/exist.yaml")
	if err != nil {
		t.Fatalf("ReadFile non-existent path should be empty, not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty tree for non-existent path, got %#v", got)
	}
}

// TestReadFileUnknownExtension rejects a file whose extension is not yaml/yml/toml.
func TestReadFileUnknownExtension(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write json: %v", err)
	}
	_, err := loader.ReadFile(p)
	if err == nil {
		t.Fatalf("expected error for .json")
	}
}

// TestEnvVarFromPath verifies the schema-walked env-var name table generation.
func TestEnvVarFromPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want string
	}{
		{"deployment.facility_id", "DEPLOYMENT_FACILITY_ID"},
		{"storage.postgres.url", "STORAGE_POSTGRES_URL"},
		{"auth.trusted_issuers.0.jwks_url", "AUTH_TRUSTED_ISSUERS_0_JWKS_URL"},
	}
	for _, c := range cases {
		got := loader.EnvVarFromPath(c.path)
		if got != c.want {
			t.Fatalf("EnvVarFromPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}
