// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package matcher_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/matcher"
)

// B-24 / P1.2: An unrecognized FHIRPath expression must default to
// fail-CLOSED. P1.2 widened the recognized shapes to .exists(),
// .empty(), and bare equality on any field; anything else still fails
// closed. A topic whose only criterion is `Patient.name.where(use =
// 'official').exists()` must NOT match any change until the sandboxed
// evaluator lands.

const fhirpathFailClosedTopic = `{
  "resourceType": "SubscriptionTopic",
  "url": "http://example.org/topics/fhirpath-fail-closed",
  "version": "1.0.0",
  "status": "active",
  "resourceTrigger": [{
    "resource": "Patient",
    "supportedInteraction": ["create", "update"],
    "fhirPathCriteria": "Patient.name.where(use = 'official').exists()"
  }]
}`

func TestEvaluateFHIRPathUnknownExpressionFailsClosed(t *testing.T) {
	t.Parallel()
	cat := loadCatalog(t, fhirpathFailClosedTopic)

	row := matcher.ResourceChange{
		ResourceType: "Patient",
		ChangeKind:   "create",
		Resource:     []byte(`{"resourceType":"Patient","id":"p1","active":true}`),
	}
	got := matcher.Evaluate(cat, row)
	if len(got) != 0 {
		t.Errorf("unknown FHIRPath must default to fail-CLOSED (no match); got %d matches", len(got))
	}
}

// OP #240: reporter regression. The "unknown expression" path used to
// fall through to a no-op when the unrecognized expression *happened*
// to end in `.exists()` — the fail-closed default returned false but
// the reporter was never invoked. Now `Patient.name.where(...).exists()`
// must both not match AND fire the reporter so the wiring layer's
// metric counts it.
func TestEvaluateFHIRPathUnknownExpression_ReporterFiresOnWherePredicate(t *testing.T) {
	cat := loadCatalog(t, fhirpathFailClosedTopic)

	var calls atomic.Int64
	var mu sync.Mutex
	var lastExpr string
	matcher.SetUnknownFHIRPathReporter(func(expr string) {
		calls.Add(1)
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
	if got := matcher.Evaluate(cat, row); len(got) != 0 {
		t.Errorf("unknown FHIRPath must fail-CLOSED; got %d matches", len(got))
	}
	if calls.Load() == 0 {
		t.Fatalf("reporter never called for unrecognized expression with .where() predicate")
	}
	mu.Lock()
	defer mu.Unlock()
	if lastExpr != "Patient.name.where(use = 'official').exists()" {
		t.Errorf("reporter received wrong expression: %q", lastExpr)
	}
}

// P1.2: .empty() is now recognized — a topic whose criterion is
// `<Resource>.<field>.empty()` fires when the field is absent and does
// NOT fire when it's present.
func TestEvaluateFHIRPathEmptyShapeRecognized(t *testing.T) {
	t.Parallel()
	const topic = `{
		"resourceType": "SubscriptionTopic",
		"url": "http://example.org/topics/empty-shape",
		"version": "1.0.0",
		"status": "active",
		"resourceTrigger": [{
			"resource": "Patient",
			"supportedInteraction": ["create"],
			"fhirPathCriteria": "Patient.deceased.empty()"
		}]
	}`
	cat := loadCatalog(t, topic)

	// Without deceased -> .empty() is true -> match.
	row := matcher.ResourceChange{
		ResourceType: "Patient",
		ChangeKind:   "create",
		Resource:     []byte(`{"resourceType":"Patient","id":"a"}`),
	}
	if got := matcher.Evaluate(cat, row); len(got) != 1 {
		t.Errorf("expected 1 match for absent deceased; got %d", len(got))
	}

	// With deceased=true -> .empty() is false -> no match.
	row.Resource = []byte(`{"resourceType":"Patient","id":"a","deceased":true}`)
	if got := matcher.Evaluate(cat, row); len(got) != 0 {
		t.Errorf("expected 0 matches when deceased present; got %d", len(got))
	}
}

// P1.2: bare equality on ANY field is recognized (was previously
// limited to `.status`). `Patient.gender = 'female'` now works.
func TestEvaluateFHIRPathBareEqualityWidened(t *testing.T) {
	t.Parallel()
	const topic = `{
		"resourceType": "SubscriptionTopic",
		"url": "http://example.org/topics/gender-eq",
		"version": "1.0.0",
		"status": "active",
		"resourceTrigger": [{
			"resource": "Patient",
			"supportedInteraction": ["create"],
			"fhirPathCriteria": "Patient.gender = 'female'"
		}]
	}`
	cat := loadCatalog(t, topic)

	row := matcher.ResourceChange{
		ResourceType: "Patient",
		ChangeKind:   "create",
		Resource:     []byte(`{"resourceType":"Patient","id":"a","gender":"female"}`),
	}
	if got := matcher.Evaluate(cat, row); len(got) != 1 {
		t.Errorf("expected match for gender=female; got %d", len(got))
	}

	row.Resource = []byte(`{"resourceType":"Patient","id":"a","gender":"male"}`)
	if got := matcher.Evaluate(cat, row); len(got) != 0 {
		t.Errorf("expected no match for gender=male; got %d", len(got))
	}
}

// And a recognized FHIRPath continues to fire.
func TestEvaluateFHIRPathRecognizedExpressionStillMatches(t *testing.T) {
	t.Parallel()
	const topic = `{
		"resourceType": "SubscriptionTopic",
		"url": "http://example.org/topics/recognized-fhirpath",
		"version": "1.0.0",
		"status": "active",
		"resourceTrigger": [{
			"resource": "Patient",
			"supportedInteraction": ["create"],
			"fhirPathCriteria": "Patient.active.exists()"
		}]
	}`
	cat := loadCatalog(t, topic)

	row := matcher.ResourceChange{
		ResourceType: "Patient",
		ChangeKind:   "create",
		Resource:     []byte(`{"resourceType":"Patient","id":"p1","active":true}`),
	}
	got := matcher.Evaluate(cat, row)
	if len(got) != 1 {
		t.Errorf("recognized FHIRPath '.exists()' must still match; got %d", len(got))
	}
}

// B-29: Concurrent atomic swap of the catalog must be torn-read-safe and
// race-detector clean.

func TestAtomicCatalogProviderRaceFree(t *testing.T) {
	t.Parallel()

	cat1 := loadCatalog(t, orderChangedTopic)
	cat2 := loadCatalog(t, orderChangedTopic)

	prov := matcher.NewAtomicCatalogProvider(cat1)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Reader: many goroutines reading the catalog concurrently.
	const readers = 8
	var readCount atomic.Int64
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				c := prov.Get()
				if c == nil {
					t.Errorf("nil catalog returned during concurrent swap")
					return
				}
				_ = c.All()
				readCount.Add(1)
			}
		}()
	}

	// Writer: swap between two catalogs many times. N-1: bumped to
	// 100k iterations from 1k so the writer cannot finish before any
	// reader has been scheduled — the previous bound was tight enough
	// to flake on busy CI runners that under-served the reader loops.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100000; i++ {
			if i%2 == 0 {
				prov.Store(cat1)
			} else {
				prov.Store(cat2)
			}
		}
		close(stop)
	}()

	wg.Wait()
	if readCount.Load() == 0 {
		t.Error("expected reader goroutines to observe at least one read")
	}
}

// AtomicCatalogProvider must satisfy the CatalogProvider interface, so
// callers can pass it straight to NewWorker.
func TestAtomicCatalogProviderUsableAsCatalogProvider(t *testing.T) {
	t.Parallel()

	cat := loadCatalog(t, orderChangedTopic)
	prov := matcher.NewAtomicCatalogProvider(cat)

	var cp matcher.CatalogProvider = prov.AsProvider()
	if cp() != cat {
		t.Errorf("AsProvider should return the same catalog handed to NewAtomicCatalogProvider")
	}
}
