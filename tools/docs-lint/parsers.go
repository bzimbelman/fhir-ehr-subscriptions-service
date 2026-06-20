// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// docFile is one markdown source plus the lint ignore sentinels declared in it.
type docFile struct {
	path    string // repo-relative
	body    string
	ignores ignoreSet
}

// ignoreSet collects per-file inline ignore sentinels. Each rule key maps to
// the set of values the doc author granted permission to skip.
type ignoreSet map[string]map[string]bool

func (s ignoreSet) allowed(rule, value string) bool {
	if s == nil {
		return false
	}
	m, ok := s[rule]
	if !ok {
		return false
	}
	return m[value]
}

// readDocFile reads a markdown file off disk and parses out its docs-lint
// ignore sentinels. A sentinel has the form
//
//	<!-- docs-lint:ignore-<rule>=<value> -->
//
// and grants the (rule, value) pair anywhere in the same file.
func readDocFile(path string) (*docFile, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return &docFile{
		path:    path,
		body:    string(body),
		ignores: parseIgnoreSentinels(string(body)),
	}, nil
}

var ignoreRE = regexp.MustCompile(`<!--\s*docs-lint:ignore-([a-z]+)=([^\s>]+)\s*-->`)

func parseIgnoreSentinels(md string) ignoreSet {
	out := ignoreSet{}
	for _, m := range ignoreRE.FindAllStringSubmatch(md, -1) {
		rule, value := m[1], m[2]
		if _, ok := out[rule]; !ok {
			out[rule] = map[string]bool{}
		}
		out[rule][value] = true
	}
	return out
}

// findMarkdownFiles returns every .md path under root, sorted, with paths
// relative to root.
func findMarkdownFiles(root string) ([]string, error) {
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// fencedCodeRE matches ``` fences open and close. We strip code blocks before
// scanning prose for things like CLI invocations, but we KEEP them when scanning
// for ports (operator examples in fenced bash blocks are exactly what we want
// to verify).
var fenceOpenRE = regexp.MustCompile("^```")

// stripFencedBlocks removes fenced code blocks from a markdown body. It returns
// only the prose (and headings).
func stripFencedBlocks(md string) string {
	var out strings.Builder
	inside := false
	scanner := bufio.NewScanner(strings.NewReader(md))
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if fenceOpenRE.MatchString(strings.TrimSpace(line)) {
			inside = !inside
			out.WriteString("\n")
			continue
		}
		if inside {
			out.WriteString("\n")
			continue
		}
		out.WriteString(line)
		out.WriteString("\n")
	}
	return out.String()
}

// fencedBlock is one ``` ... ``` extracted block with its language tag.
type fencedBlock struct {
	lang string
	body string
	line int // 1-based line number of the opening fence
}

// extractFencedBlocks returns every fenced code block in md, regardless of
// language. The opening fence's tag (after ```) becomes Lang.
func extractFencedBlocks(md string) []fencedBlock {
	lines := strings.Split(md, "\n")
	var out []fencedBlock
	i := 0
	for i < len(lines) {
		trim := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trim, "```") {
			lang := strings.TrimPrefix(trim, "```")
			start := i + 1
			j := start
			for j < len(lines) && strings.TrimSpace(lines[j]) != "```" {
				j++
			}
			out = append(out, fencedBlock{
				lang: lang,
				body: strings.Join(lines[start:j], "\n"),
				line: i + 1,
			})
			i = j + 1
			continue
		}
		i++
	}
	return out
}

// mkdocsNavLeaves walks the parsed mkdocs.yml `nav:` tree and returns the set
// of leaf paths it links to (relative to docs/). Section nodes (maps with
// children) are descended into; string leaves are added directly.
func mkdocsNavLeaves(mkdocsYAML []byte) (map[string]bool, error) {
	var root struct {
		Nav []any `yaml:"nav"`
	}
	if err := yaml.Unmarshal(mkdocsYAML, &root); err != nil {
		return nil, fmt.Errorf("parse mkdocs.yml: %w", err)
	}
	out := map[string]bool{}
	for _, n := range root.Nav {
		walkNav(n, out)
	}
	return out, nil
}

// walkNav descends the recursive YAML structure mkdocs uses for nav. Each
// node is either a string ("foo.md") or a single-key map whose value is
// either a string or a list of further nav nodes.
func walkNav(node any, out map[string]bool) {
	switch v := node.(type) {
	case string:
		out[filepath.ToSlash(v)] = true
	case map[string]any:
		for _, child := range v {
			walkNav(child, out)
		}
	case []any:
		for _, child := range v {
			walkNav(child, out)
		}
	}
}

// metricNameRE matches a fhir_subs_* metric name. The regex requires the name
// to end on a letter or digit (not a trailing underscore, which is the giveaway
// that the doc is mentioning a namespace/prefix rather than a fully-qualified
// metric — e.g. "the fhir_subs_matcher_ family of metrics").
var metricNameRE = regexp.MustCompile(`\bfhir_subs_[a-z0-9_]*[a-z0-9]\b`)

// extractCitedMetrics returns the set of fhir_subs_* names referenced anywhere
// in md. We scan the full body (prose + fenced) because operators copy the
// names from runbook examples verbatim.
//
// A token only counts as a metric citation when it has at least three
// underscore-separated segments (`fhir_subs_<sub>_<leaf>`). Bare prefixes like
// `fhir_subs_matcher` (two segments) are namespace mentions, not metrics.
func extractCitedMetrics(md string) []string {
	matches := metricNameRE.FindAllString(md, -1)
	seen := map[string]bool{}
	var out []string
	for _, m := range matches {
		// Trim trailing underscores defensively (metricNameRE already
		// rules them out, but be explicit).
		trimmed := strings.TrimRight(m, "_")
		segments := strings.Split(trimmed, "_")
		if len(segments) < 4 {
			// fhir, subs, <sub>, <leaf> — anything fewer is a namespace
			// mention.
			continue
		}
		if seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

// portRE pulls every `:NNNN` token where it could plausibly be a network port:
// at least 2 digits, at most 5, and immediately followed by `/`, end-of-line,
// whitespace, double-quote, or another non-digit. Line numbers are excluded
// elsewhere by requiring the token to live in a fenced code block or after a
// host-shaped prefix (letters / dots).
var portRE = regexp.MustCompile(`:(\d{2,5})\b`)

// hostPortRE matches `host:NNNN` shapes in prose: a sequence of letters/dots
// or `localhost` / `0.0.0.0` / `127.0.0.1` immediately before the colon.
var hostPortRE = regexp.MustCompile(`\b(?:localhost|127\.0\.0\.1|0\.0\.0\.0|[a-z][a-z0-9.\-]*[a-z0-9])(:\d{2,5})\b`)

// extractCitedPorts returns the set of port numbers referenced in md as
// network endpoints. A `:NNNN` is counted when it appears in:
//   - a fenced code block (yaml, bash, json, sh, ...) — operator examples
//   - prose with a host-shape immediately before the colon (host:port URLs)
func extractCitedPorts(md string) []string {
	seen := map[string]bool{}

	// Fenced blocks: scan every `:NNNN`.
	for _, fb := range extractFencedBlocks(md) {
		// Skip blocks whose language clearly isn't operator-runtime data
		// (mermaid diagrams, etc.).
		if isDiagramLang(fb.lang) {
			continue
		}
		for _, m := range portRE.FindAllStringSubmatch(fb.body, -1) {
			seen[m[1]] = true
		}
	}

	// Prose: only host-shaped citations.
	prose := stripFencedBlocks(md)
	for _, m := range hostPortRE.FindAllStringSubmatch(prose, -1) {
		// m[1] is the `:NNNN` group; trim the colon.
		seen[strings.TrimPrefix(m[1], ":")] = true
	}

	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func isDiagramLang(lang string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "mermaid", "plantuml", "graphviz", "dot":
		return true
	}
	return false
}

// extractCitedCLIVerbs returns the set of subcommand verbs referenced in prose
// for the `fhir-subs` CLI. A verb is the FIRST non-flag positional argument
// after `fhir-subs`. Flags (-x, --x) and inline placeholders ([options]) are
// excluded. We scan both prose and fenced blocks because operator runbooks
// cite commands in shell snippets.
func extractCitedCLIVerbs(md string) []string {
	re := regexp.MustCompile(`\bfhir-subs\b([^\n` + "`" + `]*)`)
	matches := re.FindAllStringSubmatch(md, -1)
	seen := map[string]bool{}
	var out []string
	for _, m := range matches {
		tail := m[1]
		fields := strings.Fields(tail)
		if len(fields) == 0 {
			continue
		}
		first := fields[0]
		// Strip a trailing punctuation that survived the regex (`,`, `.`,
		// `)`, `;`, `:`).
		first = strings.TrimRight(first, ",.;):`")
		if first == "" {
			continue
		}
		if strings.HasPrefix(first, "-") {
			continue
		}
		// Reject obvious placeholders ([something], <something>, ALLCAPS).
		if strings.HasPrefix(first, "[") || strings.HasPrefix(first, "<") {
			continue
		}
		// A real verb is lower-case letters with optional dashes.
		if !isVerbToken(first) {
			continue
		}
		if seen[first] {
			continue
		}
		seen[first] = true
		out = append(out, first)
	}
	sort.Strings(out)
	return out
}

func isVerbToken(tok string) bool {
	if tok == "" {
		return false
	}
	for _, r := range tok {
		switch {
		case r >= 'a' && r <= 'z':
		case r == '-':
		default:
			return false
		}
	}
	return true
}
