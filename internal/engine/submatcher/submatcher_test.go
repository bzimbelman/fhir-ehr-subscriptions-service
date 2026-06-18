// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package submatcher_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/submatcher"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// fb builds a filterBy JSON blob in the wire shape stored in
// subscriptions.filter_by per internal/api/schemas/subscription.schema.json.
func fb(clauses ...map[string]string) []byte {
	out := make([]map[string]string, 0, len(clauses))
	out = append(out, clauses...)
	b, _ := json.Marshal(out)
	return b
}

func mustResource(t *testing.T, body string) []byte {
	t.Helper()
	if !json.Valid([]byte(body)) {
		t.Fatalf("test fixture is not valid JSON: %s", body)
	}
	return []byte(body)
}

// TestEvaluateNoFilterMatches: a subscription with no filterBy on a topic
// should match every event for that topic. The Match invariant is that
// the SubscriptionMatcher is a pass-through when filterBy is empty/null.
func TestEvaluateNoFilterMatches(t *testing.T) {
	t.Parallel()
	sub := repos.SubscriptionRow{
		ID:       uuid.New(),
		TopicURL: "http://example.org/order-changed",
		Status:   repos.SubActive,
		FilterBy: nil,
	}
	ev := submatcher.EhrEvent{
		EventNumber:  100,
		TopicURL:     "http://example.org/order-changed",
		Resource:     mustResource(t, `{"resourceType":"ServiceRequest","id":"sr-1","status":"active"}`),
		ResourceType: "ServiceRequest",
		ChangeKind:   "create",
	}

	out := submatcher.Evaluate(ev, []repos.SubscriptionRow{sub})
	if len(out) != 1 {
		t.Fatalf("want 1 decision, got %d", len(out))
	}
	if out[0].Decision != submatcher.FanoutMatch {
		t.Fatalf("want Match for empty filterBy, got %v: %s", out[0].Decision, out[0].SkipReason)
	}
}

// TestEvaluateFilterByPatientMatch: a subscription with
// filterBy patient=Patient/123 matches a ServiceRequest whose subject
// is Patient/123.
func TestEvaluateFilterByPatientMatch(t *testing.T) {
	t.Parallel()
	sub := repos.SubscriptionRow{
		ID:       uuid.New(),
		TopicURL: "http://example.org/order-changed",
		Status:   repos.SubActive,
		FilterBy: fb(map[string]string{
			"resourceType":    "ServiceRequest",
			"filterParameter": "patient",
			"value":           "Patient/123",
		}),
	}
	ev := submatcher.EhrEvent{
		EventNumber:  101,
		TopicURL:     "http://example.org/order-changed",
		ResourceType: "ServiceRequest",
		ChangeKind:   "create",
		Resource:     mustResource(t, `{"resourceType":"ServiceRequest","id":"sr-1","status":"active","subject":{"reference":"Patient/123"}}`),
	}

	out := submatcher.Evaluate(ev, []repos.SubscriptionRow{sub})
	if len(out) != 1 || out[0].Decision != submatcher.FanoutMatch {
		t.Fatalf("want Match, got %#v", out)
	}
}

// TestEvaluateFilterByPatientNoMatch: a subscription with
// filterBy patient=Patient/123 does NOT match a ServiceRequest for a
// different patient. The decision must be NoMatch with a reason.
func TestEvaluateFilterByPatientNoMatch(t *testing.T) {
	t.Parallel()
	sub := repos.SubscriptionRow{
		ID:       uuid.New(),
		TopicURL: "http://example.org/order-changed",
		Status:   repos.SubActive,
		FilterBy: fb(map[string]string{
			"resourceType":    "ServiceRequest",
			"filterParameter": "patient",
			"value":           "Patient/123",
		}),
	}
	ev := submatcher.EhrEvent{
		EventNumber:  102,
		TopicURL:     "http://example.org/order-changed",
		ResourceType: "ServiceRequest",
		ChangeKind:   "create",
		Resource:     mustResource(t, `{"resourceType":"ServiceRequest","id":"sr-2","status":"active","subject":{"reference":"Patient/999"}}`),
	}

	out := submatcher.Evaluate(ev, []repos.SubscriptionRow{sub})
	if len(out) != 1 {
		t.Fatalf("want 1 decision, got %d", len(out))
	}
	if out[0].Decision != submatcher.FanoutNoMatch {
		t.Fatalf("want NoMatch, got %v", out[0].Decision)
	}
}

// TestEvaluateMultipleClausesAND: multiple filterBy clauses AND together;
// all must match for the subscription to fanout.
func TestEvaluateMultipleClausesAND(t *testing.T) {
	t.Parallel()
	sub := repos.SubscriptionRow{
		ID:       uuid.New(),
		TopicURL: "http://example.org/order-changed",
		Status:   repos.SubActive,
		FilterBy: fb(
			map[string]string{
				"resourceType":    "ServiceRequest",
				"filterParameter": "patient",
				"value":           "Patient/123",
			},
			map[string]string{
				"resourceType":    "ServiceRequest",
				"filterParameter": "status",
				"value":           "active",
			},
		),
	}
	ev := submatcher.EhrEvent{
		EventNumber:  103,
		TopicURL:     "http://example.org/order-changed",
		ResourceType: "ServiceRequest",
		ChangeKind:   "update",
		Resource:     mustResource(t, `{"resourceType":"ServiceRequest","id":"sr-3","status":"active","subject":{"reference":"Patient/123"}}`),
	}

	out := submatcher.Evaluate(ev, []repos.SubscriptionRow{sub})
	if len(out) != 1 || out[0].Decision != submatcher.FanoutMatch {
		t.Fatalf("want Match for AND of two satisfied clauses, got %#v", out)
	}

	// Flip status; must now NoMatch (one clause failed).
	ev.Resource = mustResource(t, `{"resourceType":"ServiceRequest","id":"sr-3","status":"draft","subject":{"reference":"Patient/123"}}`)
	out = submatcher.Evaluate(ev, []repos.SubscriptionRow{sub})
	if len(out) != 1 || out[0].Decision != submatcher.FanoutNoMatch {
		t.Fatalf("want NoMatch when one of AND clauses fails, got %#v", out)
	}
}

// TestEvaluateStringContainsICUFold: string :contains uses ICU root
// case+accent folding per ADR 0010 #4.
func TestEvaluateStringContainsICUFold(t *testing.T) {
	t.Parallel()
	sub := repos.SubscriptionRow{
		ID:       uuid.New(),
		TopicURL: "http://example.org/patient-name",
		Status:   repos.SubActive,
		FilterBy: fb(map[string]string{
			"resourceType":    "Patient",
			"filterParameter": "name",
			"modifier":        "contains",
			"value":           "andre", // lowercase, no diacritic
		}),
	}
	ev := submatcher.EhrEvent{
		EventNumber:  104,
		TopicURL:     "http://example.org/patient-name",
		ResourceType: "Patient",
		ChangeKind:   "update",
		// "André" with capital A and é must match "andre" under ICU root.
		Resource: mustResource(t, `{"resourceType":"Patient","id":"p-1","name":[{"text":"André Dupont"}]}`),
	}

	out := submatcher.Evaluate(ev, []repos.SubscriptionRow{sub})
	if len(out) != 1 || out[0].Decision != submatcher.FanoutMatch {
		t.Fatalf("want Match (ICU fold), got %#v", out)
	}
}

// TestEvaluateStatusNotModifier: filterBy status:not=cancelled excludes
// cancelled rows.
func TestEvaluateStatusNotModifier(t *testing.T) {
	t.Parallel()
	sub := repos.SubscriptionRow{
		ID:       uuid.New(),
		TopicURL: "http://example.org/order-changed",
		Status:   repos.SubActive,
		FilterBy: fb(map[string]string{
			"resourceType":    "ServiceRequest",
			"filterParameter": "status",
			"modifier":        "not",
			"value":           "cancelled",
		}),
	}
	ev := submatcher.EhrEvent{
		EventNumber:  105,
		TopicURL:     "http://example.org/order-changed",
		ResourceType: "ServiceRequest",
		ChangeKind:   "update",
		Resource:     mustResource(t, `{"resourceType":"ServiceRequest","id":"sr-c","status":"cancelled"}`),
	}

	out := submatcher.Evaluate(ev, []repos.SubscriptionRow{sub})
	if len(out) != 1 || out[0].Decision != submatcher.FanoutNoMatch {
		t.Fatalf("want NoMatch for status:not=cancelled vs cancelled, got %#v", out)
	}

	ev.Resource = mustResource(t, `{"resourceType":"ServiceRequest","id":"sr-a","status":"active"}`)
	out = submatcher.Evaluate(ev, []repos.SubscriptionRow{sub})
	if len(out) != 1 || out[0].Decision != submatcher.FanoutMatch {
		t.Fatalf("want Match for status:not=cancelled vs active, got %#v", out)
	}
}

// TestEvaluateTwoSubsBothMatch: two subscriptions on the same topic both
// satisfying their filterBy fan out as two Match decisions.
func TestEvaluateTwoSubsBothMatch(t *testing.T) {
	t.Parallel()
	subs := []repos.SubscriptionRow{
		{
			ID: uuid.New(), TopicURL: "http://example.org/t", Status: repos.SubActive,
			FilterBy: fb(map[string]string{"filterParameter": "patient", "value": "Patient/123"}),
		},
		{
			ID: uuid.New(), TopicURL: "http://example.org/t", Status: repos.SubActive,
			FilterBy: nil,
		},
	}
	ev := submatcher.EhrEvent{
		EventNumber:  106,
		TopicURL:     "http://example.org/t",
		ResourceType: "ServiceRequest",
		ChangeKind:   "create",
		Resource:     mustResource(t, `{"resourceType":"ServiceRequest","id":"x","subject":{"reference":"Patient/123"}}`),
	}

	out := submatcher.Evaluate(ev, subs)
	matched := 0
	for _, d := range out {
		if d.Decision == submatcher.FanoutMatch {
			matched++
		}
	}
	if matched != 2 {
		t.Fatalf("want 2 matches across the two subs, got %d (decisions=%#v)", matched, out)
	}
}

// TestEvaluateMalformedFilterByEvaluationError: a filterBy that is not
// JSON-decodable becomes an EvaluationError so the fanout loop knows to
// skip the subscription and emit the runtime-error metric. The
// subscription is NOT marked as a NoMatch — that would silently lose
// events. Per LLD: "A filter that errors at runtime produces
// EvaluationError: the matcher skips that subscription for that event,
// increments fhir_subs_filter_runtime_errors_total."
func TestEvaluateMalformedFilterByEvaluationError(t *testing.T) {
	t.Parallel()
	sub := repos.SubscriptionRow{
		ID:       uuid.New(),
		TopicURL: "http://example.org/t",
		Status:   repos.SubActive,
		FilterBy: []byte(`not-valid-json`),
	}
	ev := submatcher.EhrEvent{
		EventNumber: 107, TopicURL: "http://example.org/t", ResourceType: "X",
		Resource: []byte(`{"resourceType":"X"}`),
	}
	out := submatcher.Evaluate(ev, []repos.SubscriptionRow{sub})
	if len(out) != 1 || out[0].Decision != submatcher.FanoutEvaluationError {
		t.Fatalf("want EvaluationError on malformed filterBy, got %#v", out)
	}
}
