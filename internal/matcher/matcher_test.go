// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package matcher_test

import (
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/matcher"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/topics/catalog"
)

// orderChangedTopic exercises the seven-step algorithm:
//
//	1: resource type ServiceRequest
//	2: interactions create+update
//	3: previous status not active
//	4: current status active
//	5: requireBoth=true → AND
//	6: fhirPathCriteria absent (skipped)
//	7: emit
const orderChangedTopic = `{
  "resourceType": "SubscriptionTopic",
  "url": "http://example.org/topics/order-changed",
  "version": "1.0.0",
  "status": "active",
  "resourceTrigger": [{
    "resource": "ServiceRequest",
    "supportedInteraction": ["create", "update"],
    "queryCriteria": {
      "previous": "status:not=active",
      "current": "status=active",
      "requireBoth": true
    }
  }]
}`

func loadCatalog(t *testing.T, raw ...string) *catalog.Catalog {
	t.Helper()
	src := catalog.Sources{}
	for i, r := range raw {
		src.BuiltIn = append(src.BuiltIn, catalog.RawTopic{
			Origin: "builtin/" + string(rune('a'+i)),
			Bytes:  []byte(r),
		})
	}
	rep, err := catalog.Load(src)
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	if len(rep.Rejected) != 0 {
		t.Fatalf("unexpected rejections: %#v", rep.Rejected)
	}
	return rep.Catalog
}

func TestEvaluateMatchesWhenAllStepsPass(t *testing.T) {
	t.Parallel()
	cat := loadCatalog(t, orderChangedTopic)

	row := matcher.ResourceChange{
		ResourceType:     "ServiceRequest",
		ChangeKind:       "update",
		Resource:         []byte(`{"resourceType":"ServiceRequest","id":"a","status":"active"}`),
		PreviousResource: []byte(`{"resourceType":"ServiceRequest","id":"a","status":"draft"}`),
	}

	matches := matcher.Evaluate(cat, row)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].TopicURL != "http://example.org/topics/order-changed" {
		t.Errorf("topic URL mismatch: %q", matches[0].TopicURL)
	}
}

func TestEvaluateResourceTypeGate(t *testing.T) {
	t.Parallel()
	cat := loadCatalog(t, orderChangedTopic)

	row := matcher.ResourceChange{
		ResourceType: "Observation", // not the trigger's resource type
		ChangeKind:   "update",
		Resource:     []byte(`{"resourceType":"Observation","status":"final"}`),
	}
	if got := matcher.Evaluate(cat, row); len(got) != 0 {
		t.Errorf("expected no matches for Observation, got %d", len(got))
	}
}

func TestEvaluateInteractionGate(t *testing.T) {
	t.Parallel()
	cat := loadCatalog(t, orderChangedTopic)

	row := matcher.ResourceChange{
		ResourceType: "ServiceRequest",
		ChangeKind:   "delete", // delete not in supportedInteraction
		Resource:     []byte(`{"resourceType":"ServiceRequest","status":"active"}`),
	}
	if got := matcher.Evaluate(cat, row); len(got) != 0 {
		t.Errorf("expected no matches for delete, got %d", len(got))
	}
}

func TestEvaluateRequireBothFailsWhenPreviousMissing(t *testing.T) {
	t.Parallel()
	cat := loadCatalog(t, orderChangedTopic)

	row := matcher.ResourceChange{
		ResourceType: "ServiceRequest",
		ChangeKind:   "create",
		Resource:     []byte(`{"resourceType":"ServiceRequest","status":"active"}`),
		// no previous; requireBoth=true → must NOT match
	}
	if got := matcher.Evaluate(cat, row); len(got) != 0 {
		t.Errorf("expected 0 matches when previous absent and requireBoth=true; got %d", len(got))
	}
}

func TestEvaluateCurrentCriteriaFailsWhenStatusDoesNotMatch(t *testing.T) {
	t.Parallel()
	cat := loadCatalog(t, orderChangedTopic)

	row := matcher.ResourceChange{
		ResourceType:     "ServiceRequest",
		ChangeKind:       "update",
		Resource:         []byte(`{"resourceType":"ServiceRequest","status":"draft"}`),
		PreviousResource: []byte(`{"resourceType":"ServiceRequest","status":"unknown"}`),
	}
	// current=status=active fails (current.status="draft").
	if got := matcher.Evaluate(cat, row); len(got) != 0 {
		t.Errorf("expected no match (current status not 'active'), got %d", len(got))
	}
}

func TestEvaluateMultipleTopicsBothMatch(t *testing.T) {
	t.Parallel()

	// A second simpler topic that also fires on ServiceRequest update.
	const simpler = `{
		"resourceType": "SubscriptionTopic",
		"url": "http://example.org/topics/order-any-update",
		"version": "1.0.0",
		"status": "active",
		"resourceTrigger": [{
			"resource": "ServiceRequest",
			"supportedInteraction": ["update"]
		}]
	}`
	cat := loadCatalog(t, orderChangedTopic, simpler)

	row := matcher.ResourceChange{
		ResourceType:     "ServiceRequest",
		ChangeKind:       "update",
		Resource:         []byte(`{"resourceType":"ServiceRequest","status":"active"}`),
		PreviousResource: []byte(`{"resourceType":"ServiceRequest","status":"draft"}`),
	}
	got := matcher.Evaluate(cat, row)
	if len(got) != 2 {
		t.Fatalf("expected 2 topic matches; got %d", len(got))
	}
	urls := []string{got[0].TopicURL, got[1].TopicURL}
	if !contains(urls, "http://example.org/topics/order-changed") ||
		!contains(urls, "http://example.org/topics/order-any-update") {
		t.Errorf("expected both topics to match, got %v", urls)
	}
}

func TestEvaluateRequireBothFalseAllowsEither(t *testing.T) {
	t.Parallel()

	// requireBoth=false (default OR semantics): current matches alone is enough.
	const orTopic = `{
		"resourceType": "SubscriptionTopic",
		"url": "http://example.org/topics/either",
		"version": "1.0.0",
		"status": "active",
		"resourceTrigger": [{
			"resource": "ServiceRequest",
			"supportedInteraction": ["create", "update"],
			"queryCriteria": {
				"previous": "status=cancelled",
				"current": "status=active",
				"requireBoth": false
			}
		}]
	}`
	cat := loadCatalog(t, orTopic)

	row := matcher.ResourceChange{
		ResourceType: "ServiceRequest",
		ChangeKind:   "create",
		Resource:     []byte(`{"resourceType":"ServiceRequest","status":"active"}`),
	}
	got := matcher.Evaluate(cat, row)
	if len(got) != 1 {
		t.Errorf("requireBoth=false: current alone should match; got %d", len(got))
	}
}

func TestEvaluateStringCaseAndAccentInsensitive(t *testing.T) {
	t.Parallel()

	// ICU root-locale folding per ADR 0010 #4.
	const accentTopic = `{
		"resourceType": "SubscriptionTopic",
		"url": "http://example.org/topics/accent",
		"version": "1.0.0",
		"status": "active",
		"resourceTrigger": [{
			"resource": "Patient",
			"supportedInteraction": ["create"],
			"queryCriteria": {
				"current": "name:contains=Cafe"
			}
		}]
	}`
	cat := loadCatalog(t, accentTopic)
	row := matcher.ResourceChange{
		ResourceType: "Patient",
		ChangeKind:   "create",
		Resource:     []byte(`{"resourceType":"Patient","name":[{"text":"José Café Ñoño"}]}`),
	}
	got := matcher.Evaluate(cat, row)
	if len(got) != 1 {
		t.Errorf("expected ICU-folded :contains to match Café via Cafe; got %d matches", len(got))
	}
}

// MatchResult is what the package returns per matched topic. The test
// asserts on TopicURL above; the rest of the row is built by the
// caller from the source ResourceChange.

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// Sanity guard so CompiledTopic field renames in catalog don't go
// unnoticed.
func TestCompileTopicShape(t *testing.T) {
	t.Parallel()
	cat := loadCatalog(t, orderChangedTopic)
	all := cat.All()
	if len(all) != 1 {
		t.Fatalf("want 1 topic, got %d", len(all))
	}
	if !strings.HasPrefix(all[0].CanonicalURL, "http://") {
		t.Errorf("canonical url unexpected: %q", all[0].CanonicalURL)
	}
}
