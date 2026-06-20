// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// repoRoot returns the absolute path to the repository root assuming this test
// file lives at <repo>/tools/docs-lint/lint_test.go.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(here), "..", ".."))
}

// TestLint_NoFindings is the headline assertion: docs and code agree on every
// covered surface (CLI verbs, metrics, ports, nav). Any drift fails the test
// with an itemized list of every offending citation, scoped to the markdown
// file that hosts it. Doc authors fix the citation OR add an inline ignore
// sentinel naming the deferred symbol.
//
// This is the binary contract test for OP #263 (H8 DocsLint). Findings #40,
// #51-#74, #102, #107-#109, #121-#123, #125, #127, #135, #136, #181, #182,
// #195 surface here as either failures (still drifting) or recorded ignores
// (deferred — fix queued under another story).
func TestLint_NoFindings(t *testing.T) {
	root := repoRoot(t)
	findings, err := Lint(root)
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if len(findings) == 0 {
		return
	}
	var lines []string
	for _, f := range findings {
		lines = append(lines, f.String())
	}
	t.Fatalf("docs-lint produced %d findings (each is operator-facing drift):\n  %s",
		len(findings), strings.Join(lines, "\n  "))
}

// TestLint_DetectsFictionalCLI asserts the rule catches a fictional verb when
// it appears in a temp markdown file. This pins the parser/regex behavior so
// future refactors cannot accidentally weaken the lint.
func TestLint_DetectsFictionalCLI(t *testing.T) {
	tmp := t.TempDir()
	writeRepo(t, tmp)
	writeFile(t, filepath.Join(tmp, "docs", "fake.md"),
		"# Fake\n\nRun `fhir-subs definitely-not-a-real-verb --foo`.\n")
	// Add to nav so nav rule does not fire instead.
	appendNav(t, tmp, "fake.md")

	findings, err := Lint(tmp)
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if !hasFinding(findings, "docs/fake.md", "cli", "definitely-not-a-real-verb") {
		t.Fatalf("expected cli=definitely-not-a-real-verb finding; got:\n%s", dumpFindings(findings))
	}
}

// TestLint_DetectsFictionalMetric asserts a `fhir_subs_*` name not registered
// in any internal/* Go source is flagged.
func TestLint_DetectsFictionalMetric(t *testing.T) {
	tmp := t.TempDir()
	writeRepo(t, tmp)
	writeFile(t, filepath.Join(tmp, "docs", "fake.md"),
		"# Fake\n\nWatch `fhir_subs_definitely_not_registered_total` for spikes.\n")
	appendNav(t, tmp, "fake.md")

	findings, err := Lint(tmp)
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if !hasFinding(findings, "docs/fake.md", "metric", "fhir_subs_definitely_not_registered_total") {
		t.Fatalf("expected metric=fhir_subs_definitely_not_registered_total finding; got:\n%s", dumpFindings(findings))
	}
}

// TestLint_DetectsFictionalPort asserts a port cited in a fenced block that
// the chart does not open is flagged.
func TestLint_DetectsFictionalPort(t *testing.T) {
	tmp := t.TempDir()
	writeRepo(t, tmp)
	writeFile(t, filepath.Join(tmp, "docs", "fake.md"),
		"# Fake\n\n```bash\ncurl http://host:65000/foo\n```\n")
	appendNav(t, tmp, "fake.md")

	findings, err := Lint(tmp)
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if !hasFinding(findings, "docs/fake.md", "port", "65000") {
		t.Fatalf("expected port=65000 finding; got:\n%s", dumpFindings(findings))
	}
}

// TestLint_DetectsMissingNav asserts a docs/*.md not linked from mkdocs.yml
// surfaces as a nav finding.
func TestLint_DetectsMissingNav(t *testing.T) {
	tmp := t.TempDir()
	writeRepo(t, tmp)
	writeFile(t, filepath.Join(tmp, "docs", "orphan.md"), "# Orphan\n")
	// Note: NOT added to nav.

	findings, err := Lint(tmp)
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if !hasFinding(findings, "mkdocs.yml", "nav", "orphan.md") {
		t.Fatalf("expected nav=orphan.md finding; got:\n%s", dumpFindings(findings))
	}
}

// TestLint_HonorsIgnoreSentinel asserts an inline <!-- docs-lint:ignore-cli=foo -->
// suppresses the finding for that file/value pair.
func TestLint_HonorsIgnoreSentinel(t *testing.T) {
	tmp := t.TempDir()
	writeRepo(t, tmp)
	writeFile(t, filepath.Join(tmp, "docs", "fake.md"),
		"# Fake\n\n<!-- docs-lint:ignore-cli=imagined -->\n\nRun `fhir-subs imagined`.\n")
	appendNav(t, tmp, "fake.md")

	findings, err := Lint(tmp)
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if hasFinding(findings, "docs/fake.md", "cli", "imagined") {
		t.Fatalf("ignore sentinel did not suppress finding:\n%s", dumpFindings(findings))
	}
}

// TestLint_MetricExtraction_PrefixOnlyDoesNotTrigger asserts that a bare
// `fhir_subs_` namespace mention in prose (e.g. talking about the prefix in
// general) is not extracted as a metric — only fully-qualified names are.
func TestLint_MetricExtraction_PrefixOnlyDoesNotTrigger(t *testing.T) {
	got := extractCitedMetrics("Metrics use the `fhir_subs_` prefix. The fhir_subs_topics metric is namespaced.")
	for _, m := range got {
		// `fhir_subs_topics` has no further `_segment` so it is treated
		// as a namespace, not a metric, by extractCitedMetrics.
		if m == "fhir_subs_" {
			t.Fatalf("bare `fhir_subs_` should not surface as a metric; got %v", got)
		}
	}
}

// TestLint_RuntimeUnder10Seconds is the AC budget — the operator-facing CI
// gate must finish quickly enough to live on every PR.
func TestLint_RuntimeUnder10Seconds(t *testing.T) {
	root := repoRoot(t)
	deadline := time.Now().Add(10 * time.Second)
	if _, err := Lint(root); err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if time.Now().After(deadline) {
		t.Fatalf("Lint took longer than 10s; AC requires <10s on the operator-facing CI gate")
	}
}

// writeRepo lays down a minimum repo skeleton (go.mod stub, mkdocs.yml,
// docs/, deploy/helm/fhir-subs/values.yaml, cmd/fhir-subs/main.go,
// internal/<x>/metrics.go) so the rule lookups have data to read.
func writeRepo(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/test\n\ngo 1.22\n")
	writeFile(t, filepath.Join(root, "mkdocs.yml"),
		"site_name: test\nnav:\n  - Home: index.md\n")
	writeFile(t, filepath.Join(root, "docs", "index.md"), "# Home\n")
	writeFile(t, filepath.Join(root, "deploy", "helm", "fhir-subs", "values.yaml"),
		"service:\n  apiPort: 8443\n  probePort: 8081\n  metricsPort: 9090\n")
	writeFile(t, filepath.Join(root, "cmd", "fhir-subs", "main.go"),
		`package main

func dispatch(args []string) {
    if len(args) > 0 && args[0] == "audit" { return }
    if len(args) > 0 && args[0] == "migrate" { return }
}
`)
	// Stub a metric registration so registeredMetricNames has fixtures.
	writeFile(t, filepath.Join(root, "internal", "fixture", "metrics.go"),
		"package fixture\n\nconst Foo = \"fhir_subs_fixture_total\"\n")
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// appendNav adds a leaf entry to the test repo's mkdocs.yml so the file is
// linked from the navigation tree.
func appendNav(t *testing.T, root, leaf string) {
	t.Helper()
	path := filepath.Join(root, "mkdocs.yml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read mkdocs.yml: %v", err)
	}
	updated := string(body) + "  - Fake: " + leaf + "\n"
	writeFile(t, path, updated)
}

// hasFinding reports whether fs contains a finding matching all three keys.
func hasFinding(fs []finding, doc, rule, value string) bool {
	for _, f := range fs {
		if f.doc == doc && f.rule == rule && f.value == value {
			return true
		}
	}
	return false
}

// dumpFindings returns a sorted, multi-line render of fs for test diagnostics.
func dumpFindings(fs []finding) string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.String())
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}
