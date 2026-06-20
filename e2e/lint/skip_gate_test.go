// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// OP #259 — H4 SkipScenarioGate. This package hosts a static lint that
// refuses an unattributed t.Skip / t.Skipf call anywhere under e2e/.
// The gate runs as part of the regular Go test suite (no build tags),
// so every CI run executes it without -tags e2e.

package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFindUncitedSkips_FlagsOffender pins the contract that an
// unattributed t.Skip(...) anywhere in the scanned tree is reported.
// OP #259.
func TestFindUncitedSkips_FlagsOffender(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "offender_test.go"), `package x
import "testing"
func TestX(t *testing.T) {
	t.Skip("punt for now")
}
`)

	findings, err := FindUncitedSkips(dir, nil)
	if err != nil {
		t.Fatalf("FindUncitedSkips: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d: %s", len(findings), FormatFindings(findings))
	}
	if findings[0].Line != 4 {
		t.Errorf("want offender on line 4, got %d", findings[0].Line)
	}
	if findings[0].Call != "t.Skip" {
		t.Errorf("want t.Skip, got %s", findings[0].Call)
	}
}

// TestFindUncitedSkips_AllowsOPCitationInArg pins that an `OP #NNN`
// substring inside the call's string arg satisfies the gate. OP #259.
func TestFindUncitedSkips_AllowsOPCitationInArg(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "cited_test.go"), `package x
import "testing"
func TestX(t *testing.T) {
	t.Skip("BLOCKED ON OP #289 — legacy harness deletion")
}
func TestY(t *testing.T) {
	t.Skipf("waiting on OP #999: %v", "x")
}
`)

	findings, err := FindUncitedSkips(dir, nil)
	if err != nil {
		t.Fatalf("FindUncitedSkips: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("want 0 findings, got %d: %s", len(findings), FormatFindings(findings))
	}
}

// TestFindUncitedSkips_AllowsOPCitationInPrecedingComment pins that an
// `OP #NNN` token in a //-comment within three lines above the call
// satisfies the gate. This is the form environmental-skip annotations
// take ("// OP #259: short-mode opt-out"). OP #259.
func TestFindUncitedSkips_AllowsOPCitationInPrecedingComment(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "cited_comment_test.go"), `package x
import "testing"
func TestX(t *testing.T) {
	// OP #259: short-mode opt-out — env gate, not a feature punt.
	if testing.Short() {
		t.Skip("short")
	}
}
`)

	findings, err := FindUncitedSkips(dir, nil)
	if err != nil {
		t.Fatalf("FindUncitedSkips: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("want 0 findings, got %d: %s", len(findings), FormatFindings(findings))
	}
}

// TestFindUncitedSkips_RejectsCommentTooFarAbove pins that a citation
// six lines above a t.Skip is NOT close enough to attribute it. The
// three-line window forces the citation to live near the skip so a
// reader sees it without scrolling. OP #259.
func TestFindUncitedSkips_RejectsCommentTooFarAbove(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "far_comment_test.go"), `package x
import "testing"

// OP #259: this comment is too far from the t.Skip call below.



func TestX(t *testing.T) {
	t.Skip("punt")
}
`)

	findings, err := FindUncitedSkips(dir, nil)
	if err != nil {
		t.Fatalf("FindUncitedSkips: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d: %s", len(findings), FormatFindings(findings))
	}
}

// TestFindUncitedSkips_HonorsExcludeDirs pins that excluded directories
// are not scanned — used in the integration test below to skip the
// lint package itself (which contains intentionally-uncited fixture
// strings inside test source). OP #259.
func TestFindUncitedSkips_HonorsExcludeDirs(t *testing.T) {
	dir := t.TempDir()
	skipDir := filepath.Join(dir, "skipme")
	if err := os.MkdirAll(skipDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(skipDir, "offender_test.go"), `package skipme
import "testing"
func TestX(t *testing.T) { t.Skip("uncited") }
`)
	writeFile(t, filepath.Join(dir, "ok_test.go"), `package x
import "testing"
func TestY(t *testing.T) { t.Skip("OP #259 — fine") }
`)

	findings, err := FindUncitedSkips(dir, []string{skipDir})
	if err != nil {
		t.Fatalf("FindUncitedSkips: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("want 0 findings (skipme excluded), got %d: %s", len(findings), FormatFindings(findings))
	}
}

// TestE2EHasNoUncitedSkips is the live gate. It runs FindUncitedSkips
// against the repo's e2e/ tree (sans this lint package) and fails on
// any uncited skip. Phase C of OP #259 audits each existing skip and
// either inlines an `OP #NNN` arg or adds a preceding `// OP #NNN`
// comment. From then on, any new uncited t.Skip in e2e/ trips this
// test and blocks the merge.
func TestE2EHasNoUncitedSkips(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if _, statErr := os.Stat(root); statErr != nil {
		t.Fatalf("stat %s: %v", root, statErr)
	}
	selfDir := filepath.Join(root, "lint")

	findings, err := FindUncitedSkips(root, []string{selfDir})
	if err != nil {
		t.Fatalf("FindUncitedSkips: %v", err)
	}
	if len(findings) > 0 {
		t.Fatalf("e2e/ contains %d uncited t.Skip / t.Skipf call(s); each must reference an OpenProject ticket via `OP #NNN` in the call's string arg or in a `//` comment within 3 lines above the call:\n%s",
			len(findings), FormatFindings(findings))
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if !strings.HasSuffix(path, ".go") {
		t.Fatalf("test fixture must be a .go file: %s", path)
	}
}
