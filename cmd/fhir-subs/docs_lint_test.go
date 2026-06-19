// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestArchitectureDocConfigKeysMatchConfigStruct asserts that every YAML
// config key cited in docs/architecture.md maps to a field in the
// production Config struct. Story #162: the doc was advertising a config
// tree the loader does not parse; ~70% of documented keys were silently
// absorbed by the Extra catch-all.
//
// The test walks every ```yaml fenced block in architecture.md and, for
// each leaf key path it parses, asserts the path resolves through Config
// via yaml: struct tags. Unknown paths (silently swallowed by Extra) fail
// the test with the specific dotted path so the doc author can fix the
// promise or wire the field.
func TestArchitectureDocConfigKeysMatchConfigStruct(t *testing.T) {
	docPath := filepath.Join("..", "..", "docs", "architecture.md")
	body, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read %s: %v", docPath, err)
	}

	blocks := extractFencedYAMLBlocks(string(body))
	if len(blocks) == 0 {
		t.Fatalf("no YAML blocks found in %s — refusing to silently pass", docPath)
	}

	cfgType := reflect.TypeOf(Config{})

	var unknown []string
	seen := map[string]bool{}
	for _, block := range blocks {
		paths, err := leafKeyPaths(block.body)
		if err != nil {
			// A block we cannot parse is a doc bug too — flag it
			// rather than silently skip.
			t.Errorf("YAML block at line %d: parse: %v", block.line, err)
			continue
		}
		for _, p := range paths {
			if seen[p] {
				continue
			}
			seen[p] = true
			if !pathExistsInStruct(cfgType, p) {
				unknown = append(unknown, p)
			}
		}
	}

	if len(unknown) > 0 {
		sort.Strings(unknown)
		t.Errorf("docs/architecture.md cites %d config keys absent from cmd/fhir-subs/config.go::Config; "+
			"each key is silently swallowed by Config.Extra at runtime:\n  %s",
			len(unknown), strings.Join(unknown, "\n  "))
	}
}

// TestArchitectureDocCLISubcommandsExist asserts that every CLI form
// cited in architecture.md prose actually exists in the binary. Today
// the binary supports `fhir-subs --check-config`, `fhir-subs --version`,
// `fhir-subs --config PATH`, `fhir-subs --log-level LEVEL`,
// `fhir-subs --set KEY=VALUE`, and `fhir-subs audit verify [--from --to]`.
// Anything else cited in the doc is fiction.
func TestArchitectureDocCLISubcommandsExist(t *testing.T) {
	docPath := filepath.Join("..", "..", "docs", "architecture.md")
	body, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read %s: %v", docPath, err)
	}

	supported := supportedCLIForms()

	cited := extractCitedCLIForms(string(body))
	var unknown []string
	for _, form := range cited {
		if !supported[form] {
			unknown = append(unknown, form)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		t.Errorf("docs/architecture.md cites CLI forms not implemented in the binary:\n  %s",
			strings.Join(unknown, "\n  "))
	}
}

// supportedCLIForms is the canonical set of CLI invocations the binary
// recognizes today. Anything not in this set is considered fictional
// when cited in the docs. Update this when a new flag or subcommand
// ships in cmd/fhir-subs/main.go (or audit_cli.go).
func supportedCLIForms() map[string]bool {
	return map[string]bool{
		"fhir-subs --check-config": true,
		"fhir-subs --config":       true,
		"fhir-subs --log-level":    true,
		"fhir-subs --set":          true,
		"fhir-subs --version":      true,
		"fhir-subs --help":         true,
		"fhir-subs audit verify":   true,
	}
}

// fencedBlock is one ```yaml ... ``` block extracted from a Markdown
// file with the line number where it begins (1-based, opening fence).
type fencedBlock struct {
	line int
	body string
}

// extractFencedYAMLBlocks pulls every ```yaml fenced block out of the
// given markdown body. Blocks fenced as ```yml are accepted too. Other
// languages (mermaid, json, etc.) are ignored.
func extractFencedYAMLBlocks(md string) []fencedBlock {
	lines := strings.Split(md, "\n")
	var out []fencedBlock
	i := 0
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		if line == "```yaml" || line == "```yml" {
			start := i + 1
			j := start
			for j < len(lines) && strings.TrimSpace(lines[j]) != "```" {
				j++
			}
			body := strings.Join(lines[start:j], "\n")
			out = append(out, fencedBlock{line: i + 1, body: body})
			i = j + 1
			continue
		}
		i++
	}
	return out
}

// leafKeyPaths parses a YAML body and returns the dotted paths to every
// leaf scalar (strings, numbers, bools, nulls). List elements are
// flattened: `mllp_listener.endpoints[].name` collapses to
// `mllp_listener.endpoints.name` because reflection on a Go slice field
// recurses into the slice element type, not into a synthetic [] step.
func leafKeyPaths(body string) ([]string, error) {
	var root any
	if err := yaml.Unmarshal([]byte(body), &root); err != nil {
		return nil, err
	}
	var out []string
	collectPaths(root, "", &out)
	// Dedup: a YAML block may legitimately repeat the same key under
	// different list elements; reflection cares about the path shape
	// only.
	sort.Strings(out)
	uniq := out[:0]
	var prev string
	for _, p := range out {
		if p == prev {
			continue
		}
		uniq = append(uniq, p)
		prev = p
	}
	return uniq, nil
}

func collectPaths(v any, prefix string, out *[]string) {
	switch n := v.(type) {
	case map[string]any:
		for k, child := range n {
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			if isLeaf(child) {
				*out = append(*out, path)
			} else {
				collectPaths(child, path, out)
			}
		}
	case []any:
		// Flatten lists: descend into each element, keeping the same
		// prefix. A documented `endpoints[].name` becomes the same path
		// `endpoints.name` reflection will look for.
		for _, child := range n {
			collectPaths(child, prefix, out)
		}
	default:
		// Top-level scalar (very rare in our config blocks); record it.
		if prefix != "" {
			*out = append(*out, prefix)
		}
	}
}

func isLeaf(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return false
	default:
		return true
	}
}

// pathExistsInStruct walks a dotted YAML path through a Go struct,
// matching each segment against the struct fields' yaml: tags. Returns
// true iff the entire path resolves; an unmatched intermediate segment
// or a leaf that lands in a non-struct, non-map container fails the
// match.
//
// Special cases:
//   - A field tagged `yaml:",inline"` (Config.Extra) is the silent-
//     swallow we are trying to detect, so it deliberately does NOT match.
//   - A `map[string]T` field accepts any key as the next segment and
//     recurses into T (used for adapter.config and channels.custom-style
//     free-form sub-trees).
//   - A `[]T` slice recurses into T at the same path step (matches the
//     flattening leafKeyPaths does).
func pathExistsInStruct(t reflect.Type, path string) bool {
	if path == "" {
		return true
	}
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	parts := strings.SplitN(path, ".", 2)
	head, rest := parts[0], ""
	if len(parts) == 2 {
		rest = parts[1]
	}

	switch t.Kind() {
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := f.Tag.Get("yaml")
			name, opts := splitYAMLTag(tag)
			if opts["inline"] {
				// Inline (Config.Extra) is the silent-swallow sink we
				// explicitly do not want to count as a match.
				continue
			}
			if name == "-" {
				continue
			}
			if name == "" {
				name = strings.ToLower(f.Name)
			}
			if name == head {
				return pathExistsInStruct(f.Type, rest)
			}
		}
		return false
	case reflect.Map:
		// Free-form sub-tree (adapter.config, custom channel config,
		// http headers). Any key is acceptable; recurse into the value
		// type for the remaining path.
		return pathExistsInStruct(t.Elem(), rest)
	default:
		// Scalar reached but path remains: doc nests under a scalar.
		return rest == "" && head == ""
	}
}

func splitYAMLTag(tag string) (name string, opts map[string]bool) {
	opts = map[string]bool{}
	if tag == "" {
		return "", opts
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	for _, opt := range parts[1:] {
		opts[strings.TrimSpace(opt)] = true
	}
	return name, opts
}

// extractCitedCLIForms scans architecture.md for invocations of the
// `fhir-subs` CLI (in inline code, prose, or fenced bash blocks) and
// returns the canonical form of each: command name + first long-flag
// or first subcommand verb. We stop at the first whitespace after the
// flag/verb so argument values do not become part of the form.
//
// Examples:
//
//	`fhir-subs --check-config`        -> "fhir-subs --check-config"
//	`fhir-subs audit verify --from X` -> "fhir-subs audit verify"
//	`fhir-subs --config /etc/x`       -> "fhir-subs --config"
func extractCitedCLIForms(md string) []string {
	// Match `fhir-subs` followed by one or more whitespace-separated
	// tokens up to the first non-flag/non-verb argument.
	re := regexp.MustCompile(`\bfhir-subs\b[^\n` + "`" + `]*`)
	raw := re.FindAllString(md, -1)
	seen := map[string]bool{}
	var out []string
	for _, m := range raw {
		form := canonicalCLIForm(m)
		if form == "" {
			continue
		}
		if seen[form] {
			continue
		}
		seen[form] = true
		out = append(out, form)
	}
	sort.Strings(out)
	return out
}

// canonicalCLIForm reduces a free-text CLI invocation to "fhir-subs"
// plus either its first long flag (e.g. --check-config) or its first
// non-flag verb chain (e.g. "audit verify").
func canonicalCLIForm(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 || fields[0] != "fhir-subs" {
		return ""
	}
	if len(fields) == 1 {
		return "fhir-subs"
	}
	tok := fields[1]
	if strings.HasPrefix(tok, "--") {
		// Strip an attached =VALUE form so --config=PATH still matches.
		if eq := strings.IndexByte(tok, '='); eq >= 0 {
			tok = tok[:eq]
		}
		return "fhir-subs " + tok
	}
	if strings.HasPrefix(tok, "-") {
		return "fhir-subs " + tok
	}
	// Verb chain: include up to the first flag-looking token.
	verbs := []string{tok}
	for i := 2; i < len(fields); i++ {
		if strings.HasPrefix(fields[i], "-") {
			break
		}
		// Stop at obvious value placeholders to avoid eating noise like
		// "fhir-subs audit verify foo" where foo is a typo, not a verb.
		if !isLikelyVerb(fields[i]) {
			break
		}
		verbs = append(verbs, fields[i])
	}
	return "fhir-subs " + strings.Join(verbs, " ")
}

// isLikelyVerb returns true if the token looks like a subcommand name
// (lower-case letters, optional dashes). Argument placeholders like
// "PATH" or "[--from" are excluded.
func isLikelyVerb(tok string) bool {
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
