// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package hydration_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/hydration"
)

// fakeHydration is a deterministic in-process HydrationService for tests.
// It returns canned bodies keyed by "Type/ID" and counts every Fetch.
type fakeHydration struct {
	store  map[string][]byte
	calls  int
	failOn map[string]error
}

func (f *fakeHydration) Fetch(_ context.Context, ref spi.FhirReference) (spi.FhirResource, error) {
	f.calls++
	key := ref.ResourceType + "/" + ref.ID
	if err, ok := f.failOn[key]; ok {
		return spi.FhirResource{}, err
	}
	body, ok := f.store[key]
	if !ok {
		return spi.FhirResource{}, errors.New("not found: " + key)
	}
	return spi.FhirResource{ResourceType: ref.ResourceType, ID: ref.ID, Body: body}, nil
}

func (f *fakeHydration) CacheTTL() time.Duration { return 60 * time.Second }

func TestHydrate_AppendsIncludedResourcesAsIncludeMode(t *testing.T) {
	t.Parallel()
	fake := &fakeHydration{store: map[string][]byte{
		"Patient/p1": []byte(`{"resourceType":"Patient","id":"p1"}`),
	}}
	h := hydration.New(hydration.Config{Service: fake})

	match := spi.FhirResource{
		ResourceType: "Observation",
		ID:           "o1",
		Body:         []byte(`{"resourceType":"Observation","id":"o1","subject":{"reference":"Patient/p1"}}`),
	}
	rules := []hydration.IncludeRule{{SourceType: "Observation", Param: "subject"}}

	res, err := h.Hydrate(context.Background(), []spi.FhirResource{match}, rules)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if len(res.Match) != 1 || res.Match[0].ID != "o1" {
		t.Fatalf("match passthrough wrong: %#v", res.Match)
	}
	if len(res.Include) != 1 || res.Include[0].ResourceType != "Patient" || res.Include[0].ID != "p1" {
		t.Fatalf("include not appended: %#v", res.Include)
	}
	if fake.calls != 1 {
		t.Fatalf("expected 1 fetch, got %d", fake.calls)
	}
}

func TestHydrate_EmptyRulesReturnsMatchesOnly(t *testing.T) {
	t.Parallel()
	fake := &fakeHydration{}
	h := hydration.New(hydration.Config{Service: fake})

	match := spi.FhirResource{ResourceType: "Patient", ID: "p1", Body: []byte(`{}`)}
	res, err := h.Hydrate(context.Background(), []spi.FhirResource{match}, nil)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if len(res.Match) != 1 || len(res.Include) != 0 {
		t.Fatalf("unexpected: %#v", res)
	}
	if fake.calls != 0 {
		t.Fatalf("expected 0 fetches, got %d", fake.calls)
	}
}

func TestHydrate_DeduplicatesSameReference(t *testing.T) {
	t.Parallel()
	fake := &fakeHydration{store: map[string][]byte{
		"Patient/p1": []byte(`{"resourceType":"Patient","id":"p1"}`),
	}}
	h := hydration.New(hydration.Config{Service: fake})

	// Two Observations referencing the same Patient.
	matches := []spi.FhirResource{
		{ResourceType: "Observation", ID: "o1", Body: []byte(`{"resourceType":"Observation","id":"o1","subject":{"reference":"Patient/p1"}}`)},
		{ResourceType: "Observation", ID: "o2", Body: []byte(`{"resourceType":"Observation","id":"o2","subject":{"reference":"Patient/p1"}}`)},
	}
	rules := []hydration.IncludeRule{{SourceType: "Observation", Param: "subject"}}

	res, err := h.Hydrate(context.Background(), matches, rules)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if len(res.Include) != 1 {
		t.Fatalf("expected 1 dedup'd include, got %d (%#v)", len(res.Include), res.Include)
	}
	if fake.calls != 1 {
		t.Fatalf("expected 1 fetch (dedup), got %d", fake.calls)
	}
}

func TestHydrate_RecursiveDepthCap(t *testing.T) {
	t.Parallel()
	// Chain: Obs -> Encounter -> Patient -> ... cycles back via "subject"
	// Configure store so each level has an outbound subject reference.
	fake := &fakeHydration{store: map[string][]byte{
		"Encounter/e1":    []byte(`{"resourceType":"Encounter","id":"e1","subject":{"reference":"Patient/p1"}}`),
		"Patient/p1":      []byte(`{"resourceType":"Patient","id":"p1","subject":{"reference":"Practitioner/d1"}}`),
		"Practitioner/d1": []byte(`{"resourceType":"Practitioner","id":"d1","subject":{"reference":"Organization/o1"}}`),
		"Organization/o1": []byte(`{"resourceType":"Organization","id":"o1","subject":{"reference":"Endpoint/x1"}}`),
		"Endpoint/x1":     []byte(`{"resourceType":"Endpoint","id":"x1"}`),
	}}
	h := hydration.New(hydration.Config{Service: fake, MaxDepth: 2})

	match := spi.FhirResource{
		ResourceType: "Observation",
		ID:           "o0",
		Body:         []byte(`{"resourceType":"Observation","id":"o0","subject":{"reference":"Encounter/e1"}}`),
	}
	rules := []hydration.IncludeRule{{Param: "subject"}} // no SourceType => any

	res, err := h.Hydrate(context.Background(), []spi.FhirResource{match}, rules)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	// MaxDepth=2 means: depth-1 fetches Encounter/e1 (from match);
	// depth-2 fetches Patient/p1 (from Encounter); depth-3 would fetch
	// Practitioner but is over the cap.
	if len(res.Include) != 2 {
		t.Fatalf("MaxDepth=2 expected 2 includes, got %d (%#v)", len(res.Include), res.Include)
	}
}

func TestHydrate_TotalResourceCap(t *testing.T) {
	t.Parallel()
	// Build a fake with many distinct resources reachable from a single match.
	store := make(map[string][]byte, 50)
	body := strings.Builder{}
	body.WriteString(`{"resourceType":"DiagnosticReport","id":"d1","result":[`)
	for i := 0; i < 50; i++ {
		id := strconv.Itoa(i)
		store["Observation/"+id] = []byte(`{"resourceType":"Observation","id":"` + id + `"}`)
		if i > 0 {
			body.WriteString(",")
		}
		body.WriteString(`{"reference":"Observation/` + id + `"}`)
	}
	body.WriteString(`]}`)
	fake := &fakeHydration{store: store}
	h := hydration.New(hydration.Config{Service: fake, MaxResources: 10})

	match := spi.FhirResource{ResourceType: "DiagnosticReport", ID: "d1", Body: []byte(body.String())}
	rules := []hydration.IncludeRule{{Param: "result"}}

	res, err := h.Hydrate(context.Background(), []spi.FhirResource{match}, rules)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	// Cap is total Match+Include; match=1, so include should be capped at 9.
	total := len(res.Match) + len(res.Include)
	if total > 10 {
		t.Fatalf("total resources %d exceeded cap 10 (match=%d include=%d)",
			total, len(res.Match), len(res.Include))
	}
	if total != 10 {
		t.Fatalf("expected total=10 at the cap, got %d", total)
	}
	if len(res.Warnings) == 0 {
		t.Fatalf("expected a warning when cap was hit")
	}
}

func TestHydrate_FetchErrorDegradesWithWarning(t *testing.T) {
	t.Parallel()
	fake := &fakeHydration{
		store:  map[string][]byte{"Patient/p1": []byte(`{"resourceType":"Patient","id":"p1"}`)},
		failOn: map[string]error{"Practitioner/d1": errors.New("upstream 500")},
	}
	h := hydration.New(hydration.Config{Service: fake})

	match := spi.FhirResource{
		ResourceType: "Observation",
		ID:           "o1",
		// References: one resolvable Patient and one failing Practitioner.
		Body: []byte(`{"resourceType":"Observation","id":"o1","subject":{"reference":"Patient/p1"},"performer":[{"reference":"Practitioner/d1"}]}`),
	}
	rules := []hydration.IncludeRule{
		{SourceType: "Observation", Param: "subject"},
		{SourceType: "Observation", Param: "performer"},
	}

	res, err := h.Hydrate(context.Background(), []spi.FhirResource{match}, rules)
	if err != nil {
		t.Fatalf("Hydrate must not return error on per-ref failure: %v", err)
	}
	if len(res.Include) != 1 || res.Include[0].ID != "p1" {
		t.Fatalf("expected only Patient/p1 to land, got %#v", res.Include)
	}
	if len(res.Warnings) == 0 {
		t.Fatalf("expected a warning for failed Practitioner/d1 fetch")
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "Practitioner/d1") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("warning should mention Practitioner/d1, got %v", res.Warnings)
	}
}

func TestHydrate_RuleSourceTypeFilters(t *testing.T) {
	t.Parallel()
	fake := &fakeHydration{store: map[string][]byte{
		"Patient/p1":      []byte(`{"resourceType":"Patient","id":"p1"}`),
		"Practitioner/d1": []byte(`{"resourceType":"Practitioner","id":"d1"}`),
	}}
	h := hydration.New(hydration.Config{Service: fake})

	matches := []spi.FhirResource{
		{ResourceType: "Observation", ID: "o1", Body: []byte(`{"resourceType":"Observation","id":"o1","subject":{"reference":"Patient/p1"}}`)},
		{ResourceType: "Encounter", ID: "e1", Body: []byte(`{"resourceType":"Encounter","id":"e1","subject":{"reference":"Practitioner/d1"}}`)},
	}
	// Only run the subject include for Observation, NOT Encounter.
	rules := []hydration.IncludeRule{{SourceType: "Observation", Param: "subject"}}

	res, err := h.Hydrate(context.Background(), matches, rules)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if len(res.Include) != 1 || res.Include[0].ID != "p1" {
		t.Fatalf("expected only Patient/p1 (Encounter rule should not match), got %#v", res.Include)
	}
}

func TestHydrate_RevincludeUsesExtractor(t *testing.T) {
	t.Parallel()
	// _revinclude can't be satisfied by point Fetch alone in the MVP; the
	// caller plugs in a ReferenceExtractor that yields the inverse refs
	// (in real life, this is a search query against the EHR). For the
	// MVP we hand-roll one and assert the package routes through it.
	fake := &fakeHydration{store: map[string][]byte{
		"Observation/o1": []byte(`{"resourceType":"Observation","id":"o1"}`),
		"Observation/o2": []byte(`{"resourceType":"Observation","id":"o2"}`),
	}}
	extractor := func(res spi.FhirResource, rule hydration.IncludeRule) ([]spi.FhirReference, error) {
		if rule.Reverse && rule.Param == "subject" && res.ResourceType == "Patient" {
			return []spi.FhirReference{
				{ResourceType: "Observation", ID: "o1"},
				{ResourceType: "Observation", ID: "o2"},
			}, nil
		}
		return nil, nil
	}
	h := hydration.New(hydration.Config{Service: fake, Extractor: extractor})

	match := spi.FhirResource{ResourceType: "Patient", ID: "p1", Body: []byte(`{"resourceType":"Patient","id":"p1"}`)}
	rules := []hydration.IncludeRule{{SourceType: "Observation", Param: "subject", Reverse: true}}

	res, err := h.Hydrate(context.Background(), []spi.FhirResource{match}, rules)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if len(res.Include) != 2 {
		t.Fatalf("expected 2 revincluded Observations, got %d (%#v)", len(res.Include), res.Include)
	}
}

func TestHydrate_DoesNotRefetchMatchedResource(t *testing.T) {
	t.Parallel()
	// The match list already contains Patient/p1; if some include rule
	// extracts a reference to it, hydration must NOT re-fetch.
	fake := &fakeHydration{store: map[string][]byte{}} // empty -> any fetch fails
	h := hydration.New(hydration.Config{Service: fake})

	matches := []spi.FhirResource{
		{ResourceType: "Patient", ID: "p1", Body: []byte(`{"resourceType":"Patient","id":"p1"}`)},
		{ResourceType: "Observation", ID: "o1", Body: []byte(`{"resourceType":"Observation","id":"o1","subject":{"reference":"Patient/p1"}}`)},
	}
	rules := []hydration.IncludeRule{{SourceType: "Observation", Param: "subject"}}

	res, err := h.Hydrate(context.Background(), matches, rules)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if fake.calls != 0 {
		t.Fatalf("expected 0 fetches (Patient already in match set), got %d", fake.calls)
	}
	if len(res.Include) != 0 {
		t.Fatalf("expected no includes (already matched), got %#v", res.Include)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", res.Warnings)
	}
}
