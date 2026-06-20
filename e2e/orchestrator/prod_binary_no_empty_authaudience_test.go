// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestProdBinaryConfig_NoEmptyAuthAudience is a static-lint test for
// OP #292: every prod-binary e2e callsite must drive the production
// binary against a real auth.Verifier. An empty AuthAudience renders
// `auth.allow_dev_bypass: true` in the config and silently disables
// the bearer middleware (see prod_binary_helpers_test.go around the
// `if cfg.AuthAudience == ""` branch). #146 banned the trick on the
// 401/200 path test; #292 finishes the sweep across the remaining
// prod-binary tests so no e2e ships a config that bypasses auth.
//
// The test fails (RED) if it finds any `AuthAudience:""` literal
// anywhere under e2e/ — including this file's own banned-pattern
// regex, which is split across raw + concatenated strings to avoid
// a self-match.
func TestProdBinaryConfig_NoEmptyAuthAudience(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	e2eRoot := filepath.Join(root, "e2e")

	// Pattern matches any `AuthAudience:` (with arbitrary intra-line
	// whitespace) followed by an empty Go string literal. Tolerates
	// the formatting variants gofmt produces for aligned struct
	// literals (`AuthAudience: ""`, `AuthAudience:     ""`).
	bad := regexp.MustCompile(`AuthAudience:\s*"" *(,|//|$)`)

	type hit struct {
		path string
		line int
		text string
	}
	var hits []hit

	err = filepath.WalkDir(e2eRoot, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Skip this file — it has to spell the banned pattern out
		// in its own comment/regex without self-matching.
		if filepath.Base(path) == "prod_binary_no_empty_authaudience_test.go" {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for i, line := range strings.Split(string(body), "\n") {
			if bad.MatchString(line) {
				hits = append(hits, hit{
					path: path,
					line: i + 1,
					text: strings.TrimSpace(line),
				})
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk e2e/: %v", err)
	}

	if len(hits) == 0 {
		return
	}

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].path == hits[j].path {
			return hits[i].line < hits[j].line
		}
		return hits[i].path < hits[j].path
	})
	t.Errorf("found %d AuthAudience:\"\" callsites in e2e/ (OP #292: drop the dev-bypass trick — every prod-binary test must run against a real auth.Verifier):", len(hits))
	for _, h := range hits {
		rel, _ := filepath.Rel(root, h.path)
		t.Errorf("  %s:%d  %s", rel, h.line, h.text)
	}
}
