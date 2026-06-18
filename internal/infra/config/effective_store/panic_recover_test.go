// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package effectivestore_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	effectivestore "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/effective_store"
)

// S-15 #5: a panicking subscriber must NOT take down the dispatcher;
// every other subscriber on the same Publish must still observe the
// new snapshot.
func TestStore_PanickingSubscriber_DoesNotBreakOthers(t *testing.T) {
	t.Parallel()

	s := effectivestore.New()

	var goodCount atomic.Int32
	var goodFired sync.WaitGroup
	goodFired.Add(1)
	once := &sync.Once{}

	s.Subscribe("a", func(_ *effectivestore.Effective) {
		panic("intentional test panic")
	})
	s.Subscribe("a", func(_ *effectivestore.Effective) {
		goodCount.Add(1)
		once.Do(func() { goodFired.Done() })
	})

	s.Publish(&effectivestore.Effective{Tree: map[string]interface{}{"k": 1}})

	done := make(chan struct{})
	go func() {
		goodFired.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("good subscriber never fired; panicking subscriber broke the dispatcher")
	}

	if goodCount.Load() < 1 {
		t.Errorf("expected non-panicking subscriber to fire at least once; got %d", goodCount.Load())
	}
}
