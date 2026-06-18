// Copyright the fhir-ehr-subscriptions-service authors.
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

// P1.4: ADR 0010 #4 mandates ICU root-locale folding for ALL string
// equality, not just :contains. A subscription with `name=Müller`
// should match an event whose name field is `mUller` (or `muller`,
// `MULLER`, etc.). Today this fails because the default modifier path
// uses raw equality.
func TestEvaluateBareStringEqualityIsICUFolded(t *testing.T) {
	t.Parallel()

	const bareTopic = `{
		"resourceType": "SubscriptionTopic",
		"url": "http://example.org/topics/bare-equal",
		"version": "1.0.0",
		"status": "active",
		"resourceTrigger": [{
			"resource": "Patient",
			"supportedInteraction": ["create"],
			"queryCriteria": {
				"current": "name=Müller"
			}
		}]
	}`
	cat := loadCatalog(t, bareTopic)

	row := matcher.ResourceChange{
		ResourceType: "Patient",
		ChangeKind:   "create",
		Resource:     []byte(`{"resourceType":"Patient","name":[{"family":"mUller"}]}`),
	}
	got := matcher.Evaluate(cat, row)
	if len(got) != 1 {
		t.Errorf("expected ICU-folded bare name= to match Müller via mUller; got %d", len(got))
	}
}

// Token equality (status=ACTIVE vs status=active) is case-insensitive
// per ADR 0010 #4 once root-locale folding is applied throughout.
func TestEvaluateBareTokenEqualityIsICUFolded(t *testing.T) {
	t.Parallel()

	const tokenTopic = `{
		"resourceType": "SubscriptionTopic",
		"url": "http://example.org/topics/bare-token",
		"version": "1.0.0",
		"status": "active",
		"resourceTrigger": [{
			"resource": "ServiceRequest",
			"supportedInteraction": ["create"],
			"queryCriteria": {
				"current": "status=ACTIVE"
			}
		}]
	}`
	cat := loadCatalog(t, tokenTopic)
	row := matcher.ResourceChange{
		ResourceType: "ServiceRequest",
		ChangeKind:   "create",
		Resource:     []byte(`{"resourceType":"ServiceRequest","status":"active"}`),
	}
	got := matcher.Evaluate(cat, row)
	if len(got) != 1 {
		t.Errorf("expected case-folded status=ACTIVE to match active; got %d", len(got))
	}
}

// :not equality also folds (negated equality). A subscription with
// `status:not=ACTIVE` should still exclude an event whose status is
// `active` once folding is applied.
func TestEvaluateNotModifierIsICUFolded(t *testing.T) {
	t.Parallel()

	const notTopic = `{
		"resourceType": "SubscriptionTopic",
		"url": "http://example.org/topics/not-fold",
		"version": "1.0.0",
		"status": "active",
		"resourceTrigger": [{
			"resource": "ServiceRequest",
			"supportedInteraction": ["create"],
			"queryCriteria": {
				"current": "status:not=ACTIVE"
			}
		}]
	}`
	cat := loadCatalog(t, notTopic)
	row := matcher.ResourceChange{
		ResourceType: "ServiceRequest",
		ChangeKind:   "create",
		Resource:     []byte(`{"resourceType":"ServiceRequest","status":"active"}`),
	}
	got := matcher.Evaluate(cat, row)
	if len(got) != 0 {
		t.Errorf("expected case-folded status:not=ACTIVE to exclude active; got %d matches", len(got))
	}
}

// P1.5: Evaluate emits per-topic metrics through the host emitter.
// TopicEvaluated fires for every candidate topic; TopicMatch only when
// it fires; EvaluateDuration always observes a non-negative duration.
func TestEvaluateEmitsPerTopicMetrics(t *testing.T) {
	cat := loadCatalog(t, orderChangedTopic)

	em := &fakeMatcherMetrics{}
	matcher.SetMetricsEmitter(em)
	t.Cleanup(func() { matcher.SetMetricsEmitter(nil) })

	row := matcher.ResourceChange{
		ResourceType:     "ServiceRequest",
		ChangeKind:       "update",
		Resource:         []byte(`{"resourceType":"ServiceRequest","id":"a","status":"active"}`),
		PreviousResource: []byte(`{"resourceType":"ServiceRequest","id":"a","status":"draft"}`),
	}
	matches := matcher.Evaluate(cat, row)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match")
	}
	if em.evaluated["http://example.org/topics/order-changed"] != 1 {
		t.Errorf("topic_evaluated not fired: %#v", em.evaluated)
	}
	if em.matched["http://example.org/topics/order-changed"] != 1 {
		t.Errorf("topic_match not fired: %#v", em.matched)
	}
	if em.durations < 1 {
		t.Errorf("evaluate_duration not observed: %d", em.durations)
	}
}

type fakeMatcherMetrics struct {
	evaluated   map[string]int
	matched     map[string]int
	timeouts    map[string]int
	claimed     map[string]int
	rowAttempts map[string]int
	emitted     int
	durations   int
}

func (f *fakeMatcherMetrics) ResourceChangeClaimed(outcome string) {
	if f.claimed == nil {
		f.claimed = map[string]int{}
	}
	f.claimed[outcome]++
}
func (f *fakeMatcherMetrics) TopicEvaluated(t string) {
	if f.evaluated == nil {
		f.evaluated = map[string]int{}
	}
	f.evaluated[t]++
}
func (f *fakeMatcherMetrics) TopicMatch(t string) {
	if f.matched == nil {
		f.matched = map[string]int{}
	}
	f.matched[t]++
}
func (f *fakeMatcherMetrics) FHIRPathTimeout(t string) {
	if f.timeouts == nil {
		f.timeouts = map[string]int{}
	}
	f.timeouts[t]++
}
func (f *fakeMatcherMetrics) EvaluateDuration(string, float64) { f.durations++ }
func (f *fakeMatcherMetrics) EhrEventEmitted()                 { f.emitted++ }
func (f *fakeMatcherMetrics) RowAttempt(outcome string) {
	if f.rowAttempts == nil {
		f.rowAttempts = map[string]int{}
	}
	f.rowAttempts[outcome]++
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
