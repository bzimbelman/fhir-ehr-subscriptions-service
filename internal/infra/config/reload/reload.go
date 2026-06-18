// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package reload computes the diff between two configuration trees, partitions
// the diff by the reloadable subset, and produces a Report describing whether
// the reload is applied or rejected.
//
// Per docs/low-level-design/configuration.md S8: SIGHUP-only reload accepts
// topics.*, auth.client_registry[], deployment.log_level, delivery.retry.*,
// and channel-manifest-declared paths. Everything else requires a restart.
package reload

import (
	"reflect"
	"sort"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/schemas"
)

// Outcome enumerates the reload report classifications LLD §8 commits.
type Outcome string

// Outcome constants. Match metric label values for
// fhir_subs_config_reload_total.
const (
	OutcomeApplied            Outcome = "applied"
	OutcomeRejectedImmutable  Outcome = "rejected_immutable_change"
	OutcomeRejectedValidation Outcome = "rejected_validation"
	OutcomePartial            Outcome = "partial"
)

// Diff is one changed path between prior and next.
type Diff struct {
	Path string
	Old  interface{}
	New  interface{}
}

// Report is the structured outcome of a Plan() call.
type Report struct {
	Outcome       Outcome
	AppliedPaths  []string
	RejectedPaths []string
	// AppliedDiffs are the diffs that may be merged into the effective config.
	AppliedDiffs []Diff
}

// ComputeDiff returns the flat list of changed paths between prior and next.
// Maps recurse field-by-field; arrays / scalars are compared by reflect.DeepEqual
// at the array path (we treat arrays as opaque values for diff purposes — a
// reloadable list either replaces wholesale or it does not).
func ComputeDiff(prior, next map[string]interface{}) []Diff {
	var out []Diff
	diffMap(prior, next, "", &out)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func diffMap(prior, next map[string]interface{}, base string, out *[]Diff) {
	keys := unionKeys(prior, next)
	for _, k := range keys {
		path := joinPath(base, k)
		pv, pOk := prior[k]
		nv, nOk := next[k]
		if !pOk {
			emitAdded(nv, path, out)
			continue
		}
		if !nOk {
			emitRemoved(pv, path, out)
			continue
		}
		diffValue(pv, nv, path, out)
	}
}

// emitAdded recurses into newly-added maps so we surface a diff at every leaf
// path the operator added (rather than one opaque diff at the parent). This
// matters for reload classification: a brand-new "delivery.retry.backoff"
// section whose every leaf is reloadable must apply, not be rejected as a
// single opaque parent change.
func emitAdded(v interface{}, path string, out *[]Diff) {
	if m, ok := v.(map[string]interface{}); ok {
		for _, k := range sortedMapKeys(m) {
			emitAdded(m[k], joinPath(path, k), out)
		}
		return
	}
	*out = append(*out, Diff{Path: path, Old: nil, New: v})
}

func emitRemoved(v interface{}, path string, out *[]Diff) {
	if m, ok := v.(map[string]interface{}); ok {
		for _, k := range sortedMapKeys(m) {
			emitRemoved(m[k], joinPath(path, k), out)
		}
		return
	}
	*out = append(*out, Diff{Path: path, Old: v, New: nil})
}

func sortedMapKeys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func diffValue(prior, next interface{}, path string, out *[]Diff) {
	pm, pIsMap := prior.(map[string]interface{})
	nm, nIsMap := next.(map[string]interface{})
	if pIsMap && nIsMap {
		diffMap(pm, nm, path, out)
		return
	}
	if !reflect.DeepEqual(prior, next) {
		*out = append(*out, Diff{Path: path, Old: prior, New: next})
	}
}

func unionKeys(a, b map[string]interface{}) []string {
	seen := map[string]struct{}{}
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func joinPath(base, k string) string {
	if base == "" {
		return k
	}
	return base + "." + k
}

// PartitionByReloadable splits diffs into reloadable and non-reloadable groups
// per the registry's IsReloadable rule.
func PartitionByReloadable(diffs []Diff, r *schemas.Registry) (reloadable, immutable []Diff) {
	for _, d := range diffs {
		if r.IsReloadable(d.Path) {
			reloadable = append(reloadable, d)
		} else {
			immutable = append(immutable, d)
		}
	}
	return reloadable, immutable
}

// Plan computes the diff, partitions it, and returns the structured Report.
// LLD §8: any diff touching a non-reloadable field rejects the entire reload.
func Plan(prior, next map[string]interface{}, r *schemas.Registry) Report {
	diffs := ComputeDiff(prior, next)
	reloadable, immut := PartitionByReloadable(diffs, r)
	if len(immut) > 0 {
		report := Report{
			Outcome:       OutcomeRejectedImmutable,
			RejectedPaths: pathsOfDiffs(immut),
		}
		return report
	}
	return Report{
		Outcome:      OutcomeApplied,
		AppliedPaths: pathsOfDiffs(reloadable),
		AppliedDiffs: reloadable,
	}
}

func pathsOfDiffs(ds []Diff) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Path
	}
	sort.Strings(out)
	return out
}

// ApplyOverrides returns a fresh tree with the listed diffs applied. The prior
// tree is not mutated — the atomic swap into effective_store relies on this.
func ApplyOverrides(prior map[string]interface{}, diffs []Diff) map[string]interface{} {
	out := deepClone(prior)
	for _, d := range diffs {
		setAt(out, splitPath(d.Path), d.New)
	}
	return out
}

func splitPath(path string) []string {
	if path == "" {
		return nil
	}
	out := []string{}
	cur := ""
	for i := 0; i < len(path); i++ {
		c := path[i]
		if c == '.' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func setAt(tree map[string]interface{}, segs []string, value interface{}) {
	if len(segs) == 0 {
		return
	}
	cur := tree
	for i, s := range segs {
		if i == len(segs)-1 {
			if value == nil {
				delete(cur, s)
				return
			}
			cur[s] = value
			return
		}
		next, ok := cur[s].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			cur[s] = next
		}
		cur = next
	}
}

func deepClone(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v interface{}) interface{} {
	switch x := v.(type) {
	case map[string]interface{}:
		return deepClone(x)
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, item := range x {
			out[i] = cloneValue(item)
		}
		return out
	default:
		return v
	}
}
