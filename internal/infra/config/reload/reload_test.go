// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package reload_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/config/reload"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/config/schemas"
)

// TestComputeDiffSimpleScalar: a changed scalar surfaces one diff with its path.
func TestComputeDiffSimpleScalar(t *testing.T) {
	t.Parallel()
	prior := map[string]interface{}{
		"deployment": map[string]interface{}{"log_level": "info"},
	}
	next := map[string]interface{}{
		"deployment": map[string]interface{}{"log_level": "debug"},
	}
	diffs := reload.ComputeDiff(prior, next)
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d: %v", len(diffs), diffs)
	}
	if diffs[0].Path != "deployment.log_level" {
		t.Fatalf("path: %v", diffs[0].Path)
	}
	if diffs[0].Old != "info" || diffs[0].New != "debug" {
		t.Fatalf("old/new: %#v / %#v", diffs[0].Old, diffs[0].New)
	}
}

// TestComputeDiffArray: array additions/removals/replacements emit one diff at
// the array path (we treat arrays as opaque values for diff purposes).
func TestComputeDiffArray(t *testing.T) {
	t.Parallel()
	prior := map[string]interface{}{
		"auth": map[string]interface{}{
			"client_registry": []interface{}{
				map[string]interface{}{"id": "a"},
			},
		},
	}
	next := map[string]interface{}{
		"auth": map[string]interface{}{
			"client_registry": []interface{}{
				map[string]interface{}{"id": "a"},
				map[string]interface{}{"id": "b"},
			},
		},
	}
	diffs := reload.ComputeDiff(prior, next)
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d: %v", len(diffs), diffs)
	}
	if diffs[0].Path != "auth.client_registry" {
		t.Fatalf("path: %v", diffs[0].Path)
	}
}

// TestComputeDiffNoChange: identical trees produce no diffs.
func TestComputeDiffNoChange(t *testing.T) {
	t.Parallel()
	prior := map[string]interface{}{
		"a": map[string]interface{}{"b": 1},
	}
	next := map[string]interface{}{
		"a": map[string]interface{}{"b": 1},
	}
	if d := reload.ComputeDiff(prior, next); len(d) != 0 {
		t.Fatalf("expected no diff: %v", d)
	}
}

// TestComputeDiffNestedAddition: a brand-new field below an existing parent.
func TestComputeDiffNestedAddition(t *testing.T) {
	t.Parallel()
	prior := map[string]interface{}{
		"delivery": map[string]interface{}{
			"retry": map[string]interface{}{
				"max_attempts": 8,
			},
		},
	}
	next := map[string]interface{}{
		"delivery": map[string]interface{}{
			"retry": map[string]interface{}{
				"max_attempts": 8,
				"backoff": map[string]interface{}{
					"jitter": 0.2,
				},
			},
		},
	}
	diffs := reload.ComputeDiff(prior, next)
	paths := make([]string, len(diffs))
	for i, d := range diffs {
		paths[i] = d.Path
	}
	sort.Strings(paths)
	if !reflect.DeepEqual(paths, []string{"delivery.retry.backoff.jitter"}) {
		t.Fatalf("paths: %v", paths)
	}
}

// TestPartitionByReloadable: classify diffs as reloadable or not against the
// schemas.Registry.
func TestPartitionByReloadable(t *testing.T) {
	t.Parallel()
	r := schemas.NewRegistry()
	diffs := []reload.Diff{
		{Path: "deployment.log_level", Old: "info", New: "debug"},
		{Path: "deployment.facility_id", Old: "a", New: "b"},
		{Path: "delivery.retry.max_attempts", Old: 8, New: 4},
		{Path: "topics.catalog_dir", Old: "/etc/x", New: "/etc/y"},
		{Path: "auth.client_registry", Old: nil, New: nil},
		{Path: "server.http.bind", Old: "0.0.0.0:1", New: "0.0.0.0:2"},
	}
	rel, immut := reload.PartitionByReloadable(diffs, r)

	relPaths := pathsOf(rel)
	immPaths := pathsOf(immut)
	wantRel := []string{
		"auth.client_registry",
		"delivery.retry.max_attempts",
		"deployment.log_level",
		"topics.catalog_dir",
	}
	wantImm := []string{
		"deployment.facility_id",
		"server.http.bind",
	}
	if !reflect.DeepEqual(relPaths, wantRel) {
		t.Fatalf("rel paths:\n got=%v\nwant=%v", relPaths, wantRel)
	}
	if !reflect.DeepEqual(immPaths, wantImm) {
		t.Fatalf("immut paths:\n got=%v\nwant=%v", immPaths, wantImm)
	}
}

// TestRejectImmutable: when any diff hits an immutable path, the report is a
// rejection that lists the offending paths and applies no diffs.
func TestRejectImmutable(t *testing.T) {
	t.Parallel()
	r := schemas.NewRegistry()
	prior := map[string]interface{}{
		"server": map[string]interface{}{
			"http": map[string]interface{}{"bind": "0.0.0.0:1"},
		},
		"deployment": map[string]interface{}{"log_level": "info"},
	}
	next := map[string]interface{}{
		"server": map[string]interface{}{
			"http": map[string]interface{}{"bind": "0.0.0.0:2"},
		},
		"deployment": map[string]interface{}{"log_level": "debug"},
	}
	report := reload.Plan(prior, next, r)
	if report.Outcome != reload.OutcomeRejectedImmutable {
		t.Fatalf("expected rejected_immutable_change, got %v", report.Outcome)
	}
	rejected := strings.Join(report.RejectedPaths, ",")
	if !strings.Contains(rejected, "server.http.bind") {
		t.Fatalf("expected server.http.bind in rejected list; got %q", rejected)
	}
	if len(report.AppliedPaths) != 0 {
		t.Fatalf("rejection must apply nothing; applied=%v", report.AppliedPaths)
	}
}

// TestPlanAppliesAllReloadable: when every diff is reloadable, the plan applies them.
func TestPlanAppliesAllReloadable(t *testing.T) {
	t.Parallel()
	r := schemas.NewRegistry()
	prior := map[string]interface{}{
		"deployment": map[string]interface{}{"log_level": "info"},
		"delivery":   map[string]interface{}{"retry": map[string]interface{}{"max_attempts": 8}},
	}
	next := map[string]interface{}{
		"deployment": map[string]interface{}{"log_level": "debug"},
		"delivery":   map[string]interface{}{"retry": map[string]interface{}{"max_attempts": 4}},
	}
	report := reload.Plan(prior, next, r)
	if report.Outcome != reload.OutcomeApplied {
		t.Fatalf("expected applied, got %v", report.Outcome)
	}
	sort.Strings(report.AppliedPaths)
	want := []string{"delivery.retry.max_attempts", "deployment.log_level"}
	if !reflect.DeepEqual(report.AppliedPaths, want) {
		t.Fatalf("applied:\n got=%v\nwant=%v", report.AppliedPaths, want)
	}
}

// TestApplyOverridesAtomic: ApplyOverrides returns a fresh tree with the
// reloadable diffs applied; the prior tree is not mutated.
func TestApplyOverridesAtomic(t *testing.T) {
	t.Parallel()
	prior := map[string]interface{}{
		"deployment": map[string]interface{}{"log_level": "info"},
	}
	priorClone := deepClone(prior)
	diffs := []reload.Diff{
		{Path: "deployment.log_level", Old: "info", New: "debug"},
	}
	next := reload.ApplyOverrides(prior, diffs)
	if !reflect.DeepEqual(prior, priorClone) {
		t.Fatalf("ApplyOverrides mutated input: %#v", prior)
	}
	if next["deployment"].(map[string]interface{})["log_level"] != "debug" {
		t.Fatalf("override not applied: %#v", next)
	}
}

func pathsOf(ds []reload.Diff) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Path
	}
	sort.Strings(out)
	return out
}

func deepClone(v interface{}) map[string]interface{} {
	m := v.(map[string]interface{})
	out := map[string]interface{}{}
	for k, vv := range m {
		switch x := vv.(type) {
		case map[string]interface{}:
			out[k] = deepClone(x)
		default:
			out[k] = vv
		}
	}
	return out
}
