// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package schemas owns the per-domain JSON Schema registry. Core domain
// schemas ship embedded; adapter and channel manifests register additional
// schemas at runtime via Register.
//
// The registry walks the schemas to surface:
//   - KnownPaths       — for env-var name table generation (loader)
//   - SensitivePaths   — schema-tagged sensitive fields (validator/redaction)
//   - ReloadablePaths  — LLD §8 reloadable subset + channel manifest extras
//
// Per docs/low-level-design/configuration.md S7.
package schemas

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Registry holds compiled JSON Schemas by domain key (e.g., "deployment",
// "adapter.config", "channels.kafka").
type Registry struct {
	mu              sync.RWMutex
	compiled        map[string]*jsonschema.Schema
	raw             map[string]map[string]interface{}
	customReload    []string
	domainPathPrefx map[string]string // schema-key -> root path prefix for traversal
}

// NewRegistry returns a registry pre-loaded with the core domain schemas
// listed in LLD §7.
func NewRegistry() *Registry {
	r := &Registry{
		compiled:        map[string]*jsonschema.Schema{},
		raw:             map[string]map[string]interface{}{},
		domainPathPrefx: map[string]string{},
	}
	type core struct{ name string; data []byte }
	cores := []core{
		{"deployment", coreDeploymentJSON},
		{"server", coreServerJSON},
		{"lifecycle", coreLifecycleJSON},
		{"storage", coreStorageJSON},
		{"auth", coreAuthJSON},
		{"topics", coreTopicsJSON},
		{"delivery", coreDeliveryJSON},
		{"observability", coreObservabilityJSON},
		{"mllp_listener", coreMLLPListenerJSON},
		{"adapter", coreAdapterJSON},
		{"channels", coreChannelsJSON},
	}
	for _, c := range cores {
		if err := r.register(c.name, c.data, c.name); err != nil {
			// Embedded schemas are part of the binary; a failure here is a
			// build-time error surfaced to the developer.
			panic(fmt.Sprintf("compile core schema %q: %v", c.name, err))
		}
	}
	return r
}

// Register adds a schema for the given domain key. Returns an error if the
// schema is malformed (rejected at registration, not deferred to validate-time).
//
// The domain key is the dotted root path of the sub-tree the schema applies
// to (e.g., "adapter.config", "channels.kafka").
func (r *Registry) Register(domain string, schemaJSON []byte) error {
	return r.register(domain, schemaJSON, domain)
}

func (r *Registry) register(domain string, schemaJSON []byte, pathPrefix string) error {
	if domain == "" {
		return fmt.Errorf("schema domain key cannot be empty")
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(schemaJSON, &raw); err != nil {
		return fmt.Errorf("schema %q: malformed JSON: %w", domain, err)
	}
	c := jsonschema.NewCompiler()
	c.Draft = jsonschema.Draft2020
	resourceName := "mem://schemas/" + domain
	if err := c.AddResource(resourceName, strings.NewReader(string(schemaJSON))); err != nil {
		return fmt.Errorf("schema %q: compile add: %w", domain, err)
	}
	compiled, err := c.Compile(resourceName)
	if err != nil {
		return fmt.Errorf("schema %q: compile: %w", domain, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.compiled[domain] = compiled
	r.raw[domain] = raw
	r.domainPathPrefx[domain] = pathPrefix
	return nil
}

// Get returns the compiled schema for the given domain, or nil if absent.
func (r *Registry) Get(domain string) *jsonschema.Schema {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.compiled[domain]
}

// Domains returns the sorted list of registered domain keys.
func (r *Registry) Domains() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.compiled))
	for k := range r.compiled {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// RawSchema returns the raw JSON tree for the given domain. The validator
// uses this to walk for sensitivity / reloadable annotations.
func (r *Registry) RawSchema(domain string) map[string]interface{} {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.raw[domain]
}

// KnownPaths walks every registered schema and returns the flat list of
// dotted config paths the loader will mirror to env-var names. Paths are
// sorted for deterministic output.
//
// Schema array shapes generate path segments with explicit index 0 so env
// translation remains positional (LLD §4.2 commitment).
func (r *Registry) KnownPaths() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := map[string]struct{}{}
	for _, k := range sortedKeys(r.raw) {
		schema := r.raw[k]
		prefix := r.domainPathPrefx[k]
		walkSchemaPaths(schema, prefix, func(p string) {
			seen[p] = struct{}{}
		})
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SensitivePaths walks every registered schema and surfaces the dotted paths
// of fields annotated with `"x-sensitive": true`.
func (r *Registry) SensitivePaths() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := map[string]struct{}{}
	for _, k := range sortedKeys(r.raw) {
		schema := r.raw[k]
		prefix := r.domainPathPrefx[k]
		walkSchemaSensitive(schema, prefix, func(p string) {
			seen[p] = struct{}{}
		})
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ReloadablePaths returns the prefixes / exact paths that hot reload accepts.
// The architecture-committed subset (LLD §8) is hard-coded here; channel
// manifests can call RegisterReloadable to add more.
func (r *Registry) ReloadablePaths() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	base := []string{
		"topics.",
		"auth.client_registry",
		"deployment.log_level",
		"delivery.retry.",
	}
	out := make([]string, 0, len(base)+len(r.customReload))
	out = append(out, base...)
	out = append(out, r.customReload...)
	sort.Strings(out)
	return out
}

// RegisterReloadable adds extra reloadable paths (or path prefixes) — the
// non-connection fields a channel manifest declares.
func (r *Registry) RegisterReloadable(paths []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.customReload = append(r.customReload, paths...)
}

// IsReloadable reports whether path is covered by any of the reloadable
// patterns (exact match for a non-trailing-dot pattern; prefix match for
// a trailing-dot pattern).
func (r *Registry) IsReloadable(path string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	patterns := append([]string{
		"topics.",
		"auth.client_registry",
		"deployment.log_level",
		"delivery.retry.",
	}, r.customReload...)
	for _, p := range patterns {
		if strings.HasSuffix(p, ".") {
			if strings.HasPrefix(path, p) {
				return true
			}
		} else if path == p || strings.HasPrefix(path, p+".") {
			return true
		}
	}
	return false
}

// walkSchemaPaths walks a JSON schema document and emits each dotted path
// reachable via "properties" or array "items" (for arrays we emit ".0" so
// the env-var convention can position-index).
func walkSchemaPaths(schema map[string]interface{}, prefix string, emit func(string)) {
	if schema == nil {
		return
	}
	emit(prefix)
	if props, ok := schema["properties"].(map[string]interface{}); ok {
		for k, v := range props {
			child, _ := v.(map[string]interface{})
			next := prefix
			if next == "" {
				next = k
			} else {
				next = prefix + "." + k
			}
			walkSchemaPaths(child, next, emit)
		}
	}
	if items, ok := schema["items"].(map[string]interface{}); ok {
		// Use ".0" as the canonical positional index; env-var generation
		// produces e.g. "AUTH_TRUSTED_ISSUERS_0_ISSUER".
		next := prefix
		if next == "" {
			next = "0"
		} else {
			next = prefix + ".0"
		}
		walkSchemaPaths(items, next, emit)
	}
}

// walkSchemaSensitive emits every path whose schema node has
// "x-sensitive": true.
func walkSchemaSensitive(schema map[string]interface{}, prefix string, emit func(string)) {
	if schema == nil {
		return
	}
	if v, ok := schema["x-sensitive"]; ok {
		if b, isBool := v.(bool); isBool && b {
			emit(prefix)
		}
	}
	if props, ok := schema["properties"].(map[string]interface{}); ok {
		for k, v := range props {
			child, _ := v.(map[string]interface{})
			next := prefix
			if next == "" {
				next = k
			} else {
				next = prefix + "." + k
			}
			walkSchemaSensitive(child, next, emit)
		}
	}
	if items, ok := schema["items"].(map[string]interface{}); ok {
		next := prefix
		if next == "" {
			next = "0"
		} else {
			next = prefix + ".0"
		}
		walkSchemaSensitive(items, next, emit)
	}
}

func sortedKeys(m map[string]map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
