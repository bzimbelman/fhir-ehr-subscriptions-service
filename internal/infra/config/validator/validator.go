// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package validator runs the per-domain JSON Schema and semantic checks against
// a merged config tree.
//
// Two entry points run in this order per LLD §5:
//   - ValidateStructural — walks the registered schemas; runs BEFORE any
//     placeholder is touched. A typo never resolves a secret (LLD §3 (a)).
//   - ValidateDomainSchemas — adapter / channel manifest sub-trees once
//     secrets have been resolved.
//   - ValidateSemantic — cross-field checks: at-least-one-auth, listener
//     port collisions, etc. (LLD §13).
//
// All three return a Report that collects every error rather than stopping at
// the first.
package validator

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/schemas"
)

// Report is the result of one or more validation passes.
type Report struct {
	Errors []ValidationError
}

// ValidationError is one offending path + reason.
type ValidationError struct {
	Domain string
	Path   string
	Msg    string
}

// OK reports whether the report is error-free.
func (r Report) OK() bool { return len(r.Errors) == 0 }

// Error formats the report into a single multi-line string.
func (r Report) Error() string {
	if r.OK() {
		return ""
	}
	lines := make([]string, 0, len(r.Errors))
	for _, e := range r.Errors {
		switch {
		case e.Path != "" && e.Domain != "":
			lines = append(lines, fmt.Sprintf("[%s] %s: %s", e.Domain, e.Path, e.Msg))
		case e.Domain != "":
			lines = append(lines, fmt.Sprintf("[%s] %s", e.Domain, e.Msg))
		default:
			lines = append(lines, e.Msg)
		}
	}
	return strings.Join(lines, "\n")
}

// Add appends a new error.
func (r *Report) Add(domain, path, msg string) {
	r.Errors = append(r.Errors, ValidationError{Domain: domain, Path: path, Msg: msg})
}

// Merge appends another report's errors.
func (r *Report) Merge(other Report) { r.Errors = append(r.Errors, other.Errors...) }

// ValidateStructural walks each registered core domain schema against the
// matching sub-tree of the merged config. Runs *before* secret resolution per
// LLD §5 step 3.
//
// Unknown keys in the file are an error (no silent typos) — schemas use
// "additionalProperties: false" to surface them.
func ValidateStructural(tree map[string]interface{}, r *schemas.Registry) Report {
	var report Report
	for _, domain := range coreDomains() {
		schema := r.Get(domain)
		if schema == nil {
			continue
		}
		sub, _ := tree[domain].(map[string]interface{})
		if sub == nil {
			// Required-domain check (per architecture: deployment, server,
			// storage, adapter are hard-required). If the core schema has a
			// required-properties root, schema validation against an empty
			// object will surface it.
			sub = map[string]interface{}{}
		}
		if err := schema.Validate(sub); err != nil {
			report.Add(domain, "", err.Error())
			// In addition, walk for unknown-keys against schema's known props
			// — `additionalProperties: false` already catches this; the
			// jsonschema/v5 message identifies the offending field. We keep
			// the message verbatim so test assertions can find the typo.
		}
	}
	return report
}

// ValidateDomainSchemas validates the adapter.config sub-tree and any
// custom channel sub-trees against their manifest-registered schemas. Called
// AFTER secret resolution so resolved values are checked.
func ValidateDomainSchemas(tree map[string]interface{}, r *schemas.Registry) Report {
	var report Report
	// adapter.config
	if adapter, ok := tree["adapter"].(map[string]interface{}); ok {
		if config, ok := adapter["config"].(map[string]interface{}); ok || adapter["config"] == nil {
			if config == nil {
				config = map[string]interface{}{}
			}
			if s := r.Get("adapter.config"); s != nil {
				if err := s.Validate(config); err != nil {
					report.Add("adapter.config", "", err.Error())
				}
			}
		}
	}
	// custom channels: channels.custom[*].config validated against the
	// per-channel manifest schema registered under "channels.<id>".
	if channels, ok := tree["channels"].(map[string]interface{}); ok {
		if custom, ok := channels["custom"].([]interface{}); ok {
			for i, item := range custom {
				m, _ := item.(map[string]interface{})
				if m == nil {
					continue
				}
				id, _ := m["id"].(string)
				if id == "" {
					continue
				}
				cfg, _ := m["config"].(map[string]interface{})
				if cfg == nil {
					cfg = map[string]interface{}{}
				}
				if s := r.Get("channels." + id); s != nil {
					if err := s.Validate(cfg); err != nil {
						report.Add("channels."+id,
							fmt.Sprintf("channels.custom.%d.config", i),
							err.Error())
					}
				}
			}
		}
	}
	return report
}

// ValidateSemantic runs cross-field checks the architecture commits per
// LLD §13:
//   - auth: at least one trusted_issuer OR an authenticated client_registry;
//   - mllp_listener: no two endpoints share bind+port.
func ValidateSemantic(tree map[string]interface{}, _ *schemas.Registry) Report {
	var report Report

	// auth
	if !hasAuthEntry(tree) {
		report.Add("auth", "auth",
			"at least one auth.trusted_issuers entry OR an authenticated client_registry is required (you cannot run with no auth)")
	}

	// listener port collisions
	report.Merge(checkListenerPorts(tree))

	return report
}

func hasAuthEntry(tree map[string]interface{}) bool {
	auth, ok := tree["auth"].(map[string]interface{})
	if !ok {
		return false
	}
	if ti, ok := auth["trusted_issuers"].([]interface{}); ok && len(ti) > 0 {
		return true
	}
	if cr, ok := auth["client_registry"].([]interface{}); ok && len(cr) > 0 {
		return true
	}
	return false
}

func checkListenerPorts(tree map[string]interface{}) Report {
	var report Report
	listener, ok := tree["mllp_listener"].(map[string]interface{})
	if !ok {
		return report
	}
	endpoints, ok := listener["endpoints"].([]interface{})
	if !ok {
		return report
	}
	type seenAt struct {
		bind  string
		index int
	}
	seen := map[string]seenAt{}
	keys := make([]string, 0, len(endpoints))
	for i, e := range endpoints {
		em, _ := e.(map[string]interface{})
		if em == nil {
			continue
		}
		bind, _ := em["bind"].(string)
		if bind == "" {
			continue
		}
		if first, ok := seen[bind]; ok {
			report.Add("mllp_listener",
				fmt.Sprintf("mllp_listener.endpoints.%d.bind", i),
				fmt.Sprintf("listener bind %q collides with endpoint %d", bind, first.index))
			continue
		}
		seen[bind] = seenAt{bind: bind, index: i}
		keys = append(keys, bind)
	}
	sort.Strings(keys)
	return report
}

// coreDomains is the static order in which the validator visits core schemas.
// Sorted alphabetically for deterministic output.
func coreDomains() []string {
	return []string{
		"adapter",
		"auth",
		"channels",
		"delivery",
		"deployment",
		"lifecycle",
		"mllp_listener",
		"observability",
		"server",
		"storage",
		"topics",
	}
}
