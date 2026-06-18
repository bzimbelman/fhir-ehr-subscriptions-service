// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package matcher_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/matcher"
)

// B-24: An unrecognized FHIRPath expression must default to fail-CLOSED.
// A topic whose only criterion is `Patient.deceased.empty()` must NOT
// match any change until the sandboxed evaluator lands.

const fhirpathFailClosedTopic = `{
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

	// Writer: swap between two catalogs many times.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
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
