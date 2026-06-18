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

// TestMatcher_catalogHotReloadRace (B-29): swap the catalog 100x
// concurrently with workers iterating events. The atomic.Pointer
// inside AtomicCatalogProvider must guarantee torn-read safety.
func TestMatcher_catalogHotReloadRace(t *testing.T) {
	const topic = `{
		"resourceType": "SubscriptionTopic",
		"url": "http://example.org/topics/hot-reload",
		"version": "1.0.0",
		"status": "active",
		"resourceTrigger": [{
			"resource": "ServiceRequest",
			"supportedInteraction": ["create", "update"],
			"queryCriteria": {
				"current": "status=active"
			}
		}]
	}`

	mkCat := func() *catalog.Catalog {
		rep, err := catalog.Load(catalog.Sources{
			BuiltIn: []catalog.RawTopic{
				{Origin: "builtin/hot-reload", Bytes: []byte(topic)},
			},
		})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return rep.Catalog
	}

	cat1 := mkCat()
	cat2 := mkCat()

	prov := matcher.NewAtomicCatalogProvider(cat1)
	cp := prov.AsProvider()

	var wg sync.WaitGroup
	stop := make(chan struct{})

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
				c := cp()
				if c == nil {
					t.Errorf("CatalogProvider returned nil mid-swap")
					return
				}
				_ = matcher.Evaluate(c, matcher.ResourceChange{
					ResourceType: "ServiceRequest",
					ChangeKind:   "update",
					Resource:     []byte(`{"resourceType":"ServiceRequest","id":"a","status":"active"}`),
				})
				readCount.Add(1)
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
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
		t.Fatal("expected reader goroutines to observe at least one read")
	}
}
