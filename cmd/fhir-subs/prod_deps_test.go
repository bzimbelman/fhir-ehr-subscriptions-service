// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestProductionBinary_NoTestOnlyImports asserts that the cmd/fhir-subs
// production binary does not pull in test-only packages such as pgxmock,
// testcontainers-go, or pgregory.net/rapid. OP #213 — those packages
// belong only in *_test.go files and must never end up in the production
// dependency graph.
//
// The check uses `go list -deps ./cmd/fhir-subs` (no -test flag), which
// returns the production import closure. A failure here means somebody
// imported a test mock from a non-test file.
func TestProductionBinary_NoTestOnlyImports(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("go", "list", "-deps", "./...")
	cmd.Dir = ".."
	out, err := cmd.Output()
	if err != nil {
		// Fall back to the package itself; CI runs from repo root.
		cmd = exec.Command("go", "list", "-deps", ".")
		out, err = cmd.Output()
		if err != nil {
			t.Fatalf("go list -deps failed: %v", err)
		}
	}
	deps := strings.Split(strings.TrimSpace(string(out)), "\n")

	forbidden := map[string]string{
		"github.com/pashagolub/pgxmock":            "OP #213: pgxmock is a pgx mock; it must only appear in *_test.go files",
		"github.com/testcontainers/testcontainers": "OP #213: testcontainers-go is a test harness; it must only appear in *_test.go files",
		"pgregory.net/rapid":                       "OP #213: rapid is a property-test framework; it must only appear in *_test.go files",
	}

	var hits []string
	for _, dep := range deps {
		for prefix, reason := range forbidden {
			if strings.HasPrefix(dep, prefix) {
				hits = append(hits, dep+" — "+reason)
			}
		}
	}
	if len(hits) > 0 {
		t.Fatalf("production import closure pulls in test-only packages:\n  %s",
			strings.Join(hits, "\n  "))
	}
}

// TestProductionBinary_FhirSubs_NoTestOnlyImports narrows the same check
// to the fhir-subs binary specifically — the operator-facing artifact —
// so a regression there is loud even if a test-only dep leaks into a
// less-prominent cmd. OP #213.
func TestProductionBinary_FhirSubs_NoTestOnlyImports(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("go", "list", "-deps", ".")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps . failed: %v", err)
	}
	deps := strings.Split(strings.TrimSpace(string(out)), "\n")

	forbiddenSubstrings := []string{
		"pgxmock",
		"testcontainers",
		"pgregory.net/rapid",
	}
	var hits []string
	for _, dep := range deps {
		for _, sub := range forbiddenSubstrings {
			if strings.Contains(dep, sub) {
				hits = append(hits, dep)
			}
		}
	}
	if len(hits) > 0 {
		t.Fatalf("OP #213: cmd/fhir-subs production binary pulls in test-only packages: %v", hits)
	}
}
