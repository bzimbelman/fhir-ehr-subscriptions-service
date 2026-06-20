// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// finding is one lint error: which doc, what rule, and the offending value.
// Findings are accumulated into a sorted, stable list so CI output diffs the
// same way every run.
type finding struct {
	doc   string // repo-relative path to the markdown file
	rule  string // "cli", "metric", "port", "nav"
	value string // the offending token
	hint  string // operator-actionable suggestion
}

// String formats one finding for human consumption.
func (f finding) String() string {
	if f.hint != "" {
		return fmt.Sprintf("%s: %s=%s — %s", f.doc, f.rule, f.value, f.hint)
	}
	return fmt.Sprintf("%s: %s=%s", f.doc, f.rule, f.value)
}

// Lint walks every markdown file under root/docs and runs every rule against
// it. Findings are returned in stable order. A nil result means the docs are
// honest.
//
// root must be the repo root (the directory holding go.mod, docs/, deploy/,
// internal/, etc.).
func Lint(root string) ([]finding, error) {
	docsDir := filepath.Join(root, "docs")
	mdRel, err := findMarkdownFiles(docsDir)
	if err != nil {
		return nil, fmt.Errorf("walk docs/: %w", err)
	}
	if len(mdRel) == 0 {
		return nil, fmt.Errorf("no .md files found under %s — refusing to silently pass", docsDir)
	}

	registeredCLI, err := registeredCLIVerbs(root)
	if err != nil {
		return nil, fmt.Errorf("collect CLI verbs: %w", err)
	}
	registeredMetrics, err := registeredMetricNames(root)
	if err != nil {
		return nil, fmt.Errorf("collect metric names: %w", err)
	}
	openedPorts, err := openedPortsFromHelmValues(root)
	if err != nil {
		return nil, fmt.Errorf("collect listener ports: %w", err)
	}
	navLeaves, err := loadMkdocsNav(root)
	if err != nil {
		return nil, fmt.Errorf("parse mkdocs.yml: %w", err)
	}

	var findings []finding

	for _, rel := range mdRel {
		full := filepath.Join(docsDir, rel)
		df, err := readDocFile(full)
		if err != nil {
			return nil, err
		}

		// Engineering / audit narrative documents intentionally cite
		// finding IDs, file:line references, and historic prose that
		// look-alike port-or-CLI tokens. Skip the operator-runtime
		// rules for those files; they are not part of the
		// operator-facing surface H8 polices.
		isOperatorFacing := operatorFacingDoc(rel)

		if isOperatorFacing {
			for _, verb := range extractCitedCLIVerbs(df.body) {
				if registeredCLI[verb] {
					continue
				}
				if df.ignores.allowed("cli", verb) {
					continue
				}
				findings = append(findings, finding{
					doc:   "docs/" + rel,
					rule:  "cli",
					value: verb,
					hint:  cliHint(verb),
				})
			}
		}

		if isOperatorFacing {
			for _, metric := range extractCitedMetrics(df.body) {
				if registeredMetrics[metric] {
					continue
				}
				if df.ignores.allowed("metric", metric) {
					continue
				}
				findings = append(findings, finding{
					doc:   "docs/" + rel,
					rule:  "metric",
					value: metric,
					hint:  "metric not registered in internal/; either wire it or add <!-- docs-lint:ignore-metric=" + metric + " -->",
				})
			}
		}

		if isOperatorFacing {
			for _, port := range extractCitedPorts(df.body) {
				if openedPorts[port] {
					continue
				}
				if isWellKnownInfraPort(port) {
					continue
				}
				if df.ignores.allowed("port", port) {
					continue
				}
				findings = append(findings, finding{
					doc:   "docs/" + rel,
					rule:  "port",
					value: port,
					hint:  "port not opened by the binary (per deploy/helm/fhir-subs/values.yaml service.*Port); add a service.*Port or <!-- docs-lint:ignore-port=" + port + " -->",
				})
			}
		}
	}

	// Nav coverage: every docs/**/*.md must appear in mkdocs.yml nav OR be
	// covered by an ignore-nav sentinel in some doc. We accept ignores in
	// any file (centralizing them in docs/index.md is a reasonable
	// convention).
	allIgnoreNav := unionIgnores(docsDir, mdRel, "nav")
	for _, rel := range mdRel {
		if navLeaves[rel] {
			continue
		}
		if allIgnoreNav[rel] {
			continue
		}
		findings = append(findings, finding{
			doc:   "mkdocs.yml",
			rule:  "nav",
			value: rel,
			hint:  "docs/" + rel + " is not linked from mkdocs.yml `nav:`; add it or <!-- docs-lint:ignore-nav=" + rel + " -->",
		})
	}

	sortFindings(findings)
	return findings, nil
}

func sortFindings(fs []finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].doc != fs[j].doc {
			return fs[i].doc < fs[j].doc
		}
		if fs[i].rule != fs[j].rule {
			return fs[i].rule < fs[j].rule
		}
		return fs[i].value < fs[j].value
	})
}

// unionIgnores collects every ignore sentinel for the given rule across every
// markdown file in docs/. Used for cross-file rules like nav coverage where
// the ignore can live in a different file than the one it covers.
func unionIgnores(docsDir string, mdRel []string, rule string) map[string]bool {
	out := map[string]bool{}
	for _, rel := range mdRel {
		body, err := os.ReadFile(filepath.Join(docsDir, rel))
		if err != nil {
			continue
		}
		ig := parseIgnoreSentinels(string(body))
		for v := range ig[rule] {
			out[v] = true
		}
	}
	return out
}

// operatorFacingDoc reports whether a docs/-relative path holds operator-
// runtime guidance (architecture, runbooks, deployment recipes, demo) where
// CLI invocations and port references must resolve to real binary symbols.
//
// Engineering/audit narrative (production-readiness audits, status sweeps,
// future-work backlogs, presentation slides) is excluded: those documents
// legitimately reference finding IDs (`#51`), file:line citations
// (`subscription_handlers.go:1014`), historic plans that mention not-yet-wired
// verbs, and so on. The operator never reads them in the loop the H8 lint is
// policing.
//
// HLD / LLD design docs are forward-looking specs that name aspirational
// metrics and ports for not-yet-implemented subsystems; they are excluded too
// so that authoring a design doc is not blocked on the implementation.
// Implementation stories add the metric/port and remove the design freedom
// from the spec at the same time.
func operatorFacingDoc(rel string) bool {
	switch rel {
	case "production-readiness-audit.md",
		"production-readiness-honesty-audit.md",
		"production-readiness-honesty-audit-supplement.md",
		"production-readiness-honesty-audit-supplement-2.md",
		"production-readiness-honesty-audit-supplement-3.md",
		"status.md",
		"future-work.md",
		"presentation.md",
		"poc-completion-epic-coverage.md",
		"subscription-sidecar-demo.md":
		return false
	}
	if strings.HasPrefix(rel, "high-level-design/") {
		return false
	}
	if strings.HasPrefix(rel, "low-level-design/") {
		return false
	}
	if strings.HasPrefix(rel, "ehr-adapters-vendors/") {
		return false
	}
	return true
}

// cliHint returns a more actionable message for the well-known set of verbs
// the doc author may have intended.
func cliHint(verb string) string {
	if verb == "dead-letters" {
		return "the dead-letters subcommand is not yet wired (see C58 / Finding #40); use <!-- docs-lint:ignore-cli=dead-letters --> until then"
	}
	return "register the verb in cmd/fhir-subs/main.go's subcommand dispatch, OR mark it deferred via <!-- docs-lint:ignore-cli=" + verb + " -->"
}

// registeredCLIVerbs scans cmd/fhir-subs/main.go for the canonical subcommand
// dispatch — `args[0] == "verb"` — and returns the set of verbs the binary
// recognizes. Long-form flags (`--config`, `--check-config`, etc.) are NOT
// returned; the doc lint flags VERBS, not flags.
func registeredCLIVerbs(root string) (map[string]bool, error) {
	mainPath := filepath.Join(root, "cmd", "fhir-subs", "main.go")
	src, err := os.ReadFile(mainPath)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	// args[0] == "verb" and args[1] == "verb" — the dispatch lines in main.go.
	re := regexp.MustCompile(`args\[\d+\]\s*==\s*"([a-z][a-z0-9\-]*)"`)
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		out[m[1]] = true
	}
	// Walk every Go source file under cmd/fhir-subs to also pick up sub-verbs
	// dispatched from helper functions (audit verify, migrate up, etc.). We
	// look for the same pattern.
	cmdDir := filepath.Join(root, "cmd", "fhir-subs")
	entries, err := os.ReadDir(cmdDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".go" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(cmdDir, e.Name()))
		if err != nil {
			return nil, err
		}
		for _, m := range re.FindAllStringSubmatch(string(body), -1) {
			out[m[1]] = true
		}
	}
	return out, nil
}

// registeredMetricNames walks every Go source file under cmd/ and internal/
// and harvests every string literal that starts with "fhir_subs_" — the
// canonical name prefix for this binary's metrics. The result is the set of
// metric names the binary actually registers. A doc citation that does not
// appear here is fiction.
func registeredMetricNames(root string) (map[string]bool, error) {
	roots := []string{
		filepath.Join(root, "cmd"),
		filepath.Join(root, "internal"),
	}
	out := map[string]bool{}
	fset := token.NewFileSet()
	for _, dir := range roots {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if filepath.Ext(path) != ".go" {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				bl, ok := n.(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					return true
				}
				v, err := unquoteGoString(bl.Value)
				if err != nil {
					return true
				}
				if !strings.HasPrefix(v, "fhir_subs_") {
					return true
				}
				if !looksLikePromName(v) {
					return true
				}
				out[v] = true
				return true
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func unquoteGoString(s string) (string, error) {
	if len(s) < 2 {
		return "", fmt.Errorf("string too short")
	}
	switch s[0] {
	case '"':
		return strings.Trim(s, `"`), nil
	case '`':
		return strings.Trim(s, "`"), nil
	}
	return "", fmt.Errorf("not a string literal")
}

var promNameRE = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func looksLikePromName(s string) bool {
	if len(s) < 12 || len(s) > 200 {
		return false
	}
	return promNameRE.MatchString(s)
}

// openedPortsFromHelmValues reads the chart's default values.yaml and returns
// the set of `service.*Port` values — the ports the binary opens at runtime
// when the chart is installed with no overrides.
func openedPortsFromHelmValues(root string) (map[string]bool, error) {
	valuesPath := filepath.Join(root, "deploy", "helm", "fhir-subs", "values.yaml")
	body, err := os.ReadFile(valuesPath)
	if err != nil {
		return nil, err
	}
	var v struct {
		Service map[string]any `yaml:"service"`
	}
	if err := yaml.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("parse %s: %w", valuesPath, err)
	}
	out := map[string]bool{}
	for k, raw := range v.Service {
		if !strings.HasSuffix(k, "Port") {
			continue
		}
		switch n := raw.(type) {
		case int:
			out[fmt.Sprintf("%d", n)] = true
		case int64:
			out[fmt.Sprintf("%d", n)] = true
		case float64:
			out[fmt.Sprintf("%d", int(n))] = true
		case string:
			out[n] = true
		}
	}
	return out, nil
}

// isWellKnownInfraPort recognizes ports an operator runbook may legitimately
// reference even though the binary itself does not open them: collector
// listeners, database servers, the HTTP defaults the user is likely to
// resolve to via DNS in their cluster. Adding a port here is an explicit
// claim that the doc may cite it without our binary having opened it.
func isWellKnownInfraPort(port string) bool {
	switch port {
	case "80", "443": // HTTP/HTTPS
		return true
	case "5432": // Postgres
		return true
	case "6379": // Redis
		return true
	case "4317", "4318": // OTLP gRPC, HTTP — collector intake
		return true
	case "9090": // Prometheus convention; binary also opens this when metrics is on
		return true
	}
	return false
}

// loadMkdocsNav parses mkdocs.yml and returns the set of doc-relative paths
// linked from `nav:`.
func loadMkdocsNav(root string) (map[string]bool, error) {
	body, err := os.ReadFile(filepath.Join(root, "mkdocs.yml"))
	if err != nil {
		return nil, err
	}
	return mkdocsNavLeaves(body)
}
