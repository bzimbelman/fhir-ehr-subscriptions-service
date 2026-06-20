// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package demo holds tests that pin the demo bundle's hygiene
// requirements: no checked-in secrets, key generation tooling present,
// and operator setup steps documented.
package demo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// OP #155: the literal AES-256 codec key that previously lived at
// demo/config.yaml MUST NOT appear anywhere in the tree.
//
// The key is constructed at runtime from two halves so a CI grep
// over the repo for the joined literal only matches actual leaks,
// not this test file.
var leakedDemoKey = "bocf8udvaKT84Mk5/fLU1NHo" + "y4wf/OWbp2t7gpUm/as="

func TestDemoConfig_HasNoHardcodedAESKey(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("config.yaml")
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if strings.Contains(string(data), leakedDemoKey) {
		t.Fatalf("demo/config.yaml still contains the leaked AES key %q; OP #155 requires moving it to a ${file:...} reference",
			leakedDemoKey)
	}
}

func TestDemoConfig_ReferencesAtRestKeyViaFilePlaceholder(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("config.yaml")
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if !strings.Contains(string(data), "${file:/etc/fhir-subs/secrets/at_rest_key}") {
		t.Fatalf("demo/config.yaml must reference ${file:/etc/fhir-subs/secrets/at_rest_key} for codec.keys[].material; got:\n%s",
			string(data))
	}
}

func TestDemoSecrets_GitignoreExcludesGeneratedKey(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("secrets", ".gitignore"))
	if err != nil {
		t.Fatalf("read demo/secrets/.gitignore: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, "at_rest_key") {
		t.Fatalf("demo/secrets/.gitignore must exclude the generated at_rest_key file; got:\n%s", body)
	}
}

func TestDemoScripts_GenerateKeysIsExecutable(t *testing.T) {
	t.Parallel()
	info, err := os.Stat(filepath.Join("scripts", "generate-keys.sh"))
	if err != nil {
		t.Fatalf("stat demo/scripts/generate-keys.sh: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("demo/scripts/generate-keys.sh must be executable; got mode %v", info.Mode())
	}
	body, err := os.ReadFile(filepath.Join("scripts", "generate-keys.sh"))
	if err != nil {
		t.Fatalf("read demo/scripts/generate-keys.sh: %v", err)
	}
	if !strings.Contains(string(body), "at_rest_key") {
		t.Fatalf("demo/scripts/generate-keys.sh must produce at_rest_key; got:\n%s", string(body))
	}
}

// TestRepo_NoHardcodedDemoAESKey is the OP #155 CI guard. It walks
// the repo from this test's directory upward and fails if any file
// (other than this test, which constructs the literal at runtime)
// contains the leaked key bytes.
func TestRepo_NoHardcodedDemoAESKey(t *testing.T) {
	t.Parallel()

	// Walk from repo root.
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	selfPath, err := filepath.Abs("config_cleanup_test.go")
	if err != nil {
		t.Fatalf("abs self: %v", err)
	}

	skipDirs := map[string]bool{
		".git":         true,
		"node_modules": true,
		"vendor":       true,
	}

	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip this test file (constructs the literal at runtime).
		if path == selfPath {
			return nil
		}
		// Restrict to text-shaped extensions to keep the walk fast
		// and avoid binary false positives.
		switch ext := strings.ToLower(filepath.Ext(d.Name())); ext {
		case ".go", ".yaml", ".yml", ".md", ".json", ".sh", ".txt", ".sql", ".env":
		default:
			return nil
		}
		body, rErr := os.ReadFile(path)
		if rErr != nil {
			return nil
		}
		if strings.Contains(string(body), leakedDemoKey) {
			rel, _ := filepath.Rel(root, path)
			t.Errorf("OP #155: %s contains the leaked demo AES key — remove it", rel)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk: %v", walkErr)
	}
}

func TestDemoREADME_DocumentsKeyGeneration(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"README.md", "README-compose.md"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read demo/%s: %v", name, err)
		}
		if strings.Contains(string(data), "generate-keys.sh") {
			return
		}
	}
	t.Fatalf("OP #155: demo README files must document the generate-keys.sh setup step")
}

// OP #156: demo READMEs document the ports the publisher/subscriber and
// bridge bind to.
func TestDemoREADME_DocumentsDemoPorts(t *testing.T) {
	t.Parallel()
	want := []string{"2575", "9090"}
	missing := append([]string(nil), want...)
	for _, name := range []string{"README.md", "README-compose.md"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read demo/%s: %v", name, err)
		}
		body := string(data)
		next := missing[:0]
		for _, p := range missing {
			if !strings.Contains(body, p) {
				next = append(next, p)
			}
		}
		missing = next
	}
	if len(missing) > 0 {
		t.Fatalf("OP #156: demo README files must document ports %v; missing %v", want, missing)
	}
}
