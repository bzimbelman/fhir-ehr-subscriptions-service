// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package catalog_test

import (
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/topics/catalog"
)

// minimalTopic is a compact, syntactically valid SubscriptionTopic
// that exercises both queryCriteria forms and a fhirPathCriteria
// gate. ICU folding is exercised by the catalog string operand.
const minimalTopic = `{
  "resourceType": "SubscriptionTopic",
  "url": "http://example.org/topics/order-changed",
  "version": "1.0.0",
  "title": "Order changed",
  "status": "active",
  "resourceTrigger": [{
    "resource": "ServiceRequest",
    "supportedInteraction": ["create", "update"],
    "queryCriteria": {
      "previous": "status:not=active",
      "current": "status=active",
      "requireBoth": false
    },
    "fhirPathCriteria": "ServiceRequest.status.exists()"
  }],
  "canFilterBy": [{
    "resource": "ServiceRequest",
    "filterParameter": "patient"
  }],
  "notificationShape": [{
    "resource": "ServiceRequest",
    "include": ["ServiceRequest:patient"]
  }]
}`

func TestLoadValidatesEmbeddedSchema(t *testing.T) {
	t.Parallel()

	// Missing required field "url" must be rejected by the embedded
	// JSON Schema before any compile work runs.
	bad := `{
		"resourceType": "SubscriptionTopic",
		"version": "1.0.0",
		"status": "active",
		"resourceTrigger": [{"resource": "ServiceRequest"}]
	}`
	report, err := catalog.Load(catalog.Sources{
		Operator: []catalog.RawTopic{
			{Origin: "bad.json", Bytes: []byte(bad)},
		},
	})
	if err != nil {
		t.Fatalf("Load returned err for partial-fail: %v (expected per-topic rejection, not fatal)", err)
	}
	if len(report.Rejected) != 1 {
		t.Fatalf("expected 1 rejection, got %d", len(report.Rejected))
	}
	if !strings.Contains(report.Rejected[0].Reason, "url") {
		t.Errorf("rejection reason should mention missing url; got %q", report.Rejected[0].Reason)
	}
	if report.Catalog == nil {
		t.Fatal("expected non-nil Catalog (one bad topic should not abort the load)")
	}
	if got := len(report.Catalog.All()); got != 0 {
		t.Errorf("expected 0 active topics, got %d", got)
	}
}

func TestLoadCompilesValidTopic(t *testing.T) {
	t.Parallel()

	report, err := catalog.Load(catalog.Sources{
		BuiltIn: []catalog.RawTopic{
			{Origin: "builtin/order-changed", Bytes: []byte(minimalTopic)},
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(report.Rejected) != 0 {
		t.Fatalf("unexpected rejections: %#v", report.Rejected)
	}
	if report.Catalog == nil {
		t.Fatal("nil catalog")
	}
	all := report.Catalog.All()
	if len(all) != 1 {
		t.Fatalf("want 1 topic, got %d", len(all))
	}
	tp := all[0]
	if tp.CanonicalURL != "http://example.org/topics/order-changed" {
		t.Errorf("canonical url mismatch: %q", tp.CanonicalURL)
	}
	if tp.Version != "1.0.0" {
		t.Errorf("version mismatch: %q", tp.Version)
	}
	if len(tp.Triggers) != 1 {
		t.Fatalf("want 1 trigger, got %d", len(tp.Triggers))
	}
	tr := tp.Triggers[0]
	if !tr.ResourceTypes["ServiceRequest"] {
		t.Errorf("expected ServiceRequest in trigger ResourceTypes")
	}
	if !tr.Interactions["create"] || !tr.Interactions["update"] {
		t.Errorf("expected create+update interactions; got %#v", tr.Interactions)
	}
	if tr.PreviousCriteria == nil || tr.CurrentCriteria == nil {
		t.Error("expected both previous and current criteria compiled")
	}
	if tr.FHIRPath == "" {
		t.Error("expected fhirPathCriteria preserved")
	}
	if len(tp.FilterBy) != 1 || tp.FilterBy[0].Parameter != "patient" {
		t.Errorf("expected canFilterBy patient: %#v", tp.FilterBy)
	}
	if len(tp.NotificationShape.Includes) != 1 ||
		tp.NotificationShape.Includes[0] != "ServiceRequest:patient" {
		t.Errorf("notificationShape includes mismatch: %#v", tp.NotificationShape)
	}
}

func TestLoadIndexesByResourceType(t *testing.T) {
	t.Parallel()

	report, err := catalog.Load(catalog.Sources{
		BuiltIn: []catalog.RawTopic{
			{Origin: "builtin/order-changed", Bytes: []byte(minimalTopic)},
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := report.Catalog.ByResourceType("ServiceRequest")
	if len(got) != 1 {
		t.Errorf("expected 1 topic indexed under ServiceRequest, got %d", len(got))
	}
	if len(report.Catalog.ByResourceType("Observation")) != 0 {
		t.Error("expected no topics indexed under Observation")
	}
}

func TestLoadConflictResolutionOperatorWinsBuiltIn(t *testing.T) {
	t.Parallel()

	// Built-in and operator both contribute the same (url, version);
	// operator wins.
	overridden := strings.ReplaceAll(minimalTopic, "Order changed", "Operator override")

	report, err := catalog.Load(catalog.Sources{
		BuiltIn: []catalog.RawTopic{
			{Origin: "builtin/order-changed", Bytes: []byte(minimalTopic)},
		},
		Operator: []catalog.RawTopic{
			{Origin: "/etc/topics/order-changed.json", Bytes: []byte(overridden)},
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	all := report.Catalog.All()
	if len(all) != 1 {
		t.Fatalf("want 1 topic after merge, got %d", len(all))
	}
	if all[0].Title != "Operator override" {
		t.Errorf("operator must win over built-in; title=%q", all[0].Title)
	}
	if all[0].Source != catalog.SourceOperator {
		t.Errorf("source label wrong: %v", all[0].Source)
	}
}

func TestRequireUrlAndVersion(t *testing.T) {
	t.Parallel()

	bad := `{
		"resourceType": "SubscriptionTopic",
		"url": "http://example.org/x",
		"status": "active",
		"resourceTrigger": [{"resource": "ServiceRequest"}]
	}`
	report, err := catalog.Load(catalog.Sources{
		BuiltIn: []catalog.RawTopic{
			{Origin: "x", Bytes: []byte(bad)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rejected) != 1 {
		t.Fatalf("expected rejection for missing version, got %#v", report.Rejected)
	}
	if !strings.Contains(report.Rejected[0].Reason, "version") {
		t.Errorf("expected reason to mention version; got %q", report.Rejected[0].Reason)
	}
}

func TestRejectUnsupportedSearchParamModifier(t *testing.T) {
	t.Parallel()

	// :above is in the spec but not in our supported subset (ADR 0006).
	bad := strings.Replace(minimalTopic,
		`"current": "status=active"`,
		`"current": "status:above=active"`, 1)
	report, err := catalog.Load(catalog.Sources{
		BuiltIn: []catalog.RawTopic{
			{Origin: "x", Bytes: []byte(bad)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rejected) != 1 {
		t.Fatalf("expected rejection of :above modifier; got %#v report.Rejected", report.Rejected)
	}
	r := report.Rejected[0].Reason
	if !strings.Contains(r, "above") && !strings.Contains(r, "modifier") &&
		!strings.Contains(r, "unsupported") {
		t.Errorf("expected reason to flag unsupported modifier; got %q", r)
	}
}
