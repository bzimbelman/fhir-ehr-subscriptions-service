// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/topics/catalog"
)

// TestMatcher_topicOverride (B-25): if an operator-supplied override
// has the same (url, version) as a working built-in but fails to
// compile, the catalog must keep the built-in active and surface an
// Override record so an operator typo cannot silently shadow a
// working topic.
func TestMatcher_topicOverride(t *testing.T) {
	const builtIn = `{
		"resourceType": "SubscriptionTopic",
		"url": "http://example.org/topics/order-changed",
		"version": "1.0.0",
		"title": "Built-in (working)",
		"status": "active",
		"resourceTrigger": [{
			"resource": "ServiceRequest",
			"supportedInteraction": ["create", "update"],
			"queryCriteria": {
				"current": "status=active"
			}
		}]
	}`

	const operatorBroken = `{
		"resourceType": "SubscriptionTopic",
		"url": "http://example.org/topics/order-changed",
		"version": "1.0.0",
		"title": "Operator override (broken)",
		"status": "active",
		"resourceTrigger": [{
			"resource": "ServiceRequest",
			"supportedInteraction": ["create", "update"],
			"queryCriteria": {
				"current": "performer=Practitioner/123"
			}
		}]
	}`

	rep, err := catalog.Load(catalog.Sources{
		BuiltIn: []catalog.RawTopic{
			{Origin: "builtin/order-changed", Bytes: []byte(builtIn)},
		},
		Operator: []catalog.RawTopic{
			{Origin: "/etc/topics/order-changed.json", Bytes: []byte(operatorBroken)},
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	all := rep.Catalog.All()
	if len(all) != 1 {
		t.Fatalf("expected 1 active topic (built-in survives), got %d", len(all))
	}
	if all[0].Title != "Built-in (working)" {
		t.Errorf("expected built-in to remain active after operator override fails to compile; got title=%q", all[0].Title)
	}
	if all[0].Source != catalog.SourceBuiltIn {
		t.Errorf("expected SourceBuiltIn after fallback; got %v", all[0].Source)
	}
	if got := len(rep.Rejected); got != 1 {
		t.Fatalf("expected the broken operator override to be rejected; got %d rejections", got)
	}
	if got := len(rep.Overridden); got != 1 {
		t.Fatalf("expected 1 Override record; got %d", got)
	}
	o := rep.Overridden[0]
	if o.URL != "http://example.org/topics/order-changed" {
		t.Errorf("override URL mismatch: %q", o.URL)
	}
	if o.ToSource != catalog.SourceOperator {
		t.Errorf("override ToSource should be the rejected operator candidate; got %v", o.ToSource)
	}
	if o.FromSource != catalog.SourceBuiltIn {
		t.Errorf("override FromSource should be the surviving built-in; got %v", o.FromSource)
	}
	if !strings.Contains(o.Reason, "performer") {
		t.Errorf("override reason should preserve the original rejection reason; got %q", o.Reason)
	}
	cat := rep.Catalog
	if got := len(cat.Overridden()); got != 1 {
		t.Errorf("Catalog.Overridden() should mirror Report.Overridden; got %d entries", got)
	}
}
