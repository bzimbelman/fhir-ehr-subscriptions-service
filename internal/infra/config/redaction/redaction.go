// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package redaction owns the redaction map: every field tagged sensitive at
// parse time, keyed by *config path* (not by string blacklist). Every
// serializer that produces logs, errors, $status, or metric labels reads
// through Redact() so sensitive values never appear verbatim.
//
// Per docs/low-level-design/configuration.md S9: redaction is path-keyed and
// structural, not a downstream string filter.
package redaction

import (
	"strconv"
	"sync"
)

// Map records which config paths hold sensitive values. Concurrent-safe.
//
// A path is a dotted, slice-indexed accessor — e.g.,
// "storage.postgres.url", "auth.client_registry.0.jwks_url". The Path helper
// builds those consistently across the module.
type Map struct {
	mu  sync.RWMutex
	set map[string]struct{}
}

// NewMap returns an empty redaction map.
func NewMap() *Map {
	return &Map{set: map[string]struct{}{}}
}

// TagSensitive marks path as sensitive. Idempotent.
func (m *Map) TagSensitive(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.set[path] = struct{}{}
}

// IsSensitive reports whether path was tagged sensitive.
func (m *Map) IsSensitive(path string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.set[path]
	return ok
}

// Paths returns a snapshot of the current sensitive-path set. Useful for
// diagnostics and metrics (see fhir_subs_config_redacted_fields gauge).
func (m *Map) Paths() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.set))
	for k := range m.set {
		out = append(out, k)
	}
	return out
}

// Len returns the number of paths tagged sensitive.
func (m *Map) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.set)
}

// Clone returns a deep copy. Used so Reload can build a candidate redaction
// map without disturbing the live one until the atomic swap commits.
func (m *Map) Clone() *Map {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c := &Map{set: make(map[string]struct{}, len(m.set))}
	for k := range m.set {
		c.set[k] = struct{}{}
	}
	return c
}

// JoinPath appends a string segment to base, with the standard "." separator
// (or starting fresh if base is empty).
func JoinPath(base, segment string) string {
	if base == "" {
		return segment
	}
	return base + "." + segment
}

// JoinIndex appends an integer index segment to base.
func JoinIndex(base string, index int) string {
	return JoinPath(base, strconv.Itoa(index))
}

// Redacted is the sentinel string substituted for any sensitive value at
// serialization time.
const Redacted = "[redacted]"

// MaxRedactDepth caps the recursion depth of Redact. YAML / JSON inputs
// nesting deeper than this collapse to a sentinel rather than blowing
// the stack. The value is generous (operators rarely exceed 32 levels);
// it exists to bound a hostile / malformed config tree, not to constrain
// reasonable schemas. S-15 #3.
const MaxRedactDepth = 256

// RedactedTooDeep is returned in place of the subtree when MaxRedactDepth
// is exceeded.
const RedactedTooDeep = "[redacted: depth-limited]"

// Redact walks v and substitutes Redacted for every value whose path was
// tagged sensitive in m. Maps and slices recurse; scalars at sensitive
// paths become the sentinel. The returned tree is a fresh structure — the
// caller's tree is not mutated.
//
// Recursion is depth-capped at MaxRedactDepth so a maliciously nested
// YAML document cannot stack-overflow the redactor.
func (m *Map) Redact(v interface{}, basePath string) interface{} {
	return m.redact(v, basePath, 0)
}

func (m *Map) redact(v interface{}, basePath string, depth int) interface{} {
	if depth > MaxRedactDepth {
		return RedactedTooDeep
	}
	if m.IsSensitive(basePath) {
		return Redacted
	}
	switch x := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, vv := range x {
			out[k] = m.redact(vv, JoinPath(basePath, k), depth+1)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, item := range x {
			out[i] = m.redact(item, JoinIndex(basePath, i), depth+1)
		}
		return out
	default:
		return v
	}
}

// MergeFrom set-unions other's tagged paths into m. Used by the reload path
// to build a candidate redaction map from a fresh schema walk plus newly
// resolved placeholders, then atomic-swap it into the snapshot.
func (m *Map) MergeFrom(other *Map) {
	if other == nil {
		return
	}
	other.mu.RLock()
	defer other.mu.RUnlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range other.set {
		m.set[k] = struct{}{}
	}
}
