// AST-walker tests for prod-stub-lint. These tests pin the four
// detection rules against real Go source files under testdata/. They
// drive go/parser + go/ast directly — no mocking.
package main

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// findingsByRule groups Findings by their Rule code so individual
// rules can be asserted without coupling to ordering.
func findingsByRule(fs []Finding) map[string][]Finding {
	out := make(map[string][]Finding)
	for _, f := range fs {
		out[f.Rule] = append(out[f.Rule], f)
	}
	return out
}

func filesIn(fs []Finding) []string {
	seen := map[string]struct{}{}
	for _, f := range fs {
		seen[filepath.Base(f.File)] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// F50 — forbidden no-op stub identifiers in non-test files under
// production roots.
func TestRule_F50_FlagsForbiddenStubsInProdNonTestFile(t *testing.T) {
	dir := filepath.Join("testdata", "forbidden_idents", "notest")
	got, err := WalkProductionTrees([]string{dir})
	if err != nil {
		t.Fatalf("WalkProductionTrees: %v", err)
	}
	by := findingsByRule(got)
	f50 := by["F50"]
	if len(f50) == 0 {
		t.Fatalf("expected at least one F50 finding, got %d total findings: %#v", len(got), got)
	}
	wantIdents := map[string]bool{
		"defaultActivator":     false,
		"noopReplayer":         false,
		"stubChannelActivator": false,
	}
	for _, f := range f50 {
		for ident := range wantIdents {
			if strings.Contains(f.Message, ident) {
				wantIdents[ident] = true
			}
		}
	}
	for ident, seen := range wantIdents {
		if !seen {
			t.Errorf("F50: expected a finding mentioning %q, none seen; findings=%#v", ident, f50)
		}
	}
}

// F50 — must NOT flag the same identifiers when they appear only in
// *_test.go files. Test files are intentional homes for stub
// activators while H1/H10 migration is in flight.
func TestRule_F50_AllowsForbiddenStubsInTestFiles(t *testing.T) {
	dir := filepath.Join("testdata", "forbidden_idents", "withtest")
	got, err := WalkProductionTrees([]string{dir})
	if err != nil {
		t.Fatalf("WalkProductionTrees: %v", err)
	}
	for _, f := range got {
		if f.Rule == "F50" {
			t.Errorf("F50 must not fire on a *_test.go file, but did: %#v", f)
		}
	}
}

// F49 — interface-type self-conversion of nil placeholder
// (bare-ident form `var _ Channel = Channel(nil)`).
func TestRule_F49_FlagsInterfaceNilPlaceholder(t *testing.T) {
	got, err := WalkProductionTrees([]string{filepath.Join("testdata", "iface_nil_placeholder")})
	if err != nil {
		t.Fatalf("WalkProductionTrees: %v", err)
	}
	var bare []Finding
	for _, f := range got {
		if f.Rule == "F49" && strings.HasSuffix(f.File, "placeholder.go") {
			bare = append(bare, f)
		}
	}
	if len(bare) != 1 {
		t.Fatalf("expected exactly 1 F49 finding on placeholder.go, got %d: %#v", len(bare), bare)
	}
	if !strings.Contains(bare[0].Message, "Channel") {
		t.Errorf("F49 message should mention the interface type, got: %q", bare[0].Message)
	}
}

// F49 — must fire on the selector-form placeholder
// `var _ channel.Channel = channel.Channel(nil)` (the canonical shape
// from cmd/fhir-subs/wiring.go pre-cleanup).
func TestRule_F49_FlagsSelectorFormPlaceholder(t *testing.T) {
	dir := filepath.Join("testdata", "iface_nil_placeholder", "pkgform")
	got, err := WalkProductionTrees([]string{dir})
	if err != nil {
		t.Fatalf("WalkProductionTrees: %v", err)
	}
	by := findingsByRule(got)
	f49 := by["F49"]
	if len(f49) != 1 {
		t.Fatalf("expected exactly 1 F49 finding for selector-form placeholder, got %d: %#v", len(f49), got)
	}
	if !strings.Contains(f49[0].Message, "channel.Channel") {
		t.Errorf("F49 message should mention the qualified interface type `channel.Channel`, got: %q", f49[0].Message)
	}
}

// F49 — must NOT fire on legitimate `var _ I = (*Concrete)(nil)`
// interface assertions.
func TestRule_F49_AllowsConcreteInterfaceAssertion(t *testing.T) {
	dir := filepath.Join("testdata", "iface_nil_placeholder")
	got, err := WalkProductionTrees([]string{dir})
	if err != nil {
		t.Fatalf("WalkProductionTrees: %v", err)
	}
	for _, f := range got {
		if f.Rule == "F49" && strings.Contains(f.Message, "RealChannel") {
			t.Errorf("F49 must not fire on `var _ Channel = (*RealChannel)(nil)`, but did: %#v", f)
		}
	}
}

// F119 — production code under cmd/ or internal/ MUST NOT import a
// path containing the segment "/e2e/".
func TestRule_F119_FlagsE2EImportFromProdCode(t *testing.T) {
	dir := filepath.Join("testdata", "e2e_imports")
	got, err := WalkProductionTrees([]string{
		filepath.Join(dir, "cmd"),
		filepath.Join(dir, "internal"),
	})
	if err != nil {
		t.Fatalf("WalkProductionTrees: %v", err)
	}
	by := findingsByRule(got)
	f119 := by["F119"]
	if len(f119) != 1 {
		t.Fatalf("expected exactly 1 F119 finding (cmd/demo importing e2e/sub), got %d: %#v", len(f119), f119)
	}
	if !strings.Contains(f119[0].File, "demo") {
		t.Errorf("F119 should be on cmd/demo file, got: %q", f119[0].File)
	}
	if !strings.Contains(f119[0].Message, "/e2e/") {
		t.Errorf("F119 message should reference the offending /e2e/ import path, got: %q", f119[0].Message)
	}
}

// F119 — clean internal package with no e2e/ imports must surface
// nothing.
func TestRule_F119_CleanInternalPackagePasses(t *testing.T) {
	dir := filepath.Join("testdata", "e2e_imports", "internal")
	got, err := WalkProductionTrees([]string{dir})
	if err != nil {
		t.Fatalf("WalkProductionTrees: %v", err)
	}
	for _, f := range got {
		if f.Rule == "F119" {
			t.Errorf("F119 must not fire on clean internal/clean fixture, but did: %#v", f)
		}
	}
}

// Whole-fixture clean cases — when production roots contain only
// clean code, the lint must report zero findings.
func TestClean_NoFindings(t *testing.T) {
	dir := filepath.Join("testdata", "clean")
	got, err := WalkProductionTrees([]string{
		filepath.Join(dir, "cmd"),
		filepath.Join(dir, "internal"),
	})
	if err != nil {
		t.Fatalf("WalkProductionTrees: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected zero findings on clean fixture, got %d on files=%v: %#v", len(got), filesIn(got), got)
	}
}
