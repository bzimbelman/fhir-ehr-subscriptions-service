// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/matcher"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/topics/catalog"
)

// TestMatcher_fhirpathFailClosed (B-24): a topic whose FHIRPath uses a
// shape the matcher cannot evaluate must NOT match. Earlier behavior
// fell through to `return true`, firing every event.
func TestMatcher_fhirpathFailClosed(t *testing.T) {
	const topic = `{
		"resourceType": "SubscriptionTopic",
		"url": "http://example.org/topics/fhirpath-fail-closed",
		"version": "1.0.0",
		"status": "active",
		"resourceTrigger": [{
			"resource": "Patient",
			"supportedInteraction": ["create", "update"],
			"fhirPathCriteria": "Patient.deceased.empty()"
		}]
	}`

	rep, err := catalog.Load(catalog.Sources{
		BuiltIn: []catalog.RawTopic{
			{Origin: "builtin/fail-closed", Bytes: []byte(topic)},
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(rep.Rejected) != 0 {
		t.Fatalf("topic should compile (FHIRPath text is preserved); got rejections: %#v", rep.Rejected)
	}

	var unknownCount atomic.Int64
	var mu sync.Mutex
	var lastExpr string
	matcher.SetUnknownFHIRPathReporter(func(expr string) {
		unknownCount.Add(1)
		mu.Lock()
		lastExpr = expr
		mu.Unlock()
	})
	t.Cleanup(func() { matcher.SetUnknownFHIRPathReporter(nil) })

	row := matcher.ResourceChange{
		ResourceType: "Patient",
		ChangeKind:   "create",
		Resource:     []byte(`{"resourceType":"Patient","id":"p1","active":true}`),
	}
	matches := matcher.Evaluate(rep.Catalog, row)
	if len(matches) != 0 {
		t.Errorf("unknown FHIRPath shape must default to fail-CLOSED; got %d matches", len(matches))
	}
	if unknownCount.Load() == 0 {
		t.Error("unknown-FHIRPath reporter should have been invoked")
	}
	mu.Lock()
	defer mu.Unlock()
	if lastExpr != "Patient.deceased.empty()" {
		t.Errorf("reporter should receive verbatim expression; got %q", lastExpr)
	}
}
