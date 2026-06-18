// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package effectivestore_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	effectivestore "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/effective_store"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/redaction"
)

// TestPublishReadAtomic: a Read after Publish returns the published snapshot.
func TestPublishReadAtomic(t *testing.T) {
	t.Parallel()
	s := effectivestore.New()
	if s.Read() != nil {
		t.Fatalf("read before publish should be nil")
	}
	eff := &effectivestore.Effective{
		Tree:      map[string]interface{}{"a": 1},
		Redaction: redaction.NewMap(),
	}
	s.Publish(eff)
	if s.Read() != eff {
		t.Fatalf("Read did not return the published pointer")
	}
}

// TestSubscribeFiresAfterPublish: a registered subscriber is invoked with the
// new snapshot after Publish.
func TestSubscribeFiresAfterPublish(t *testing.T) {
	t.Parallel()
	s := effectivestore.New()
	var got atomic.Pointer[effectivestore.Effective]
	done := make(chan struct{})
	s.Subscribe("delivery", func(e *effectivestore.Effective) {
		got.Store(e)
		close(done)
	})
	eff := &effectivestore.Effective{Tree: map[string]interface{}{}}
	s.Publish(eff)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("subscriber not invoked")
	}
	if got.Load() != eff {
		t.Fatalf("subscriber received wrong snapshot")
	}
}

// TestUnsubscribe: an Unsubscribed callback does not fire on subsequent Publish.
func TestUnsubscribe(t *testing.T) {
	t.Parallel()
	s := effectivestore.New()
	var fired atomic.Int32
	id := s.Subscribe("delivery", func(_ *effectivestore.Effective) {
		fired.Add(1)
	})
	s.Unsubscribe("delivery", id)
	s.Publish(&effectivestore.Effective{})
	// Notifications go through goroutines; give them a moment to (not) run.
	time.Sleep(50 * time.Millisecond)
	if fired.Load() != 0 {
		t.Fatalf("unsubscribed callback fired %d times", fired.Load())
	}
}

// TestSlowSubscriberDoesNotBlock: a subscriber that blocks indefinitely does
// not block other subscribers from being notified.
func TestSlowSubscriberDoesNotBlock(t *testing.T) {
	t.Parallel()
	s := effectivestore.New()
	slow := make(chan struct{})
	defer close(slow)
	s.Subscribe("a", func(_ *effectivestore.Effective) {
		<-slow
	})
	fast := make(chan struct{})
	s.Subscribe("a", func(_ *effectivestore.Effective) {
		close(fast)
	})
	s.Publish(&effectivestore.Effective{})
	select {
	case <-fast:
	case <-time.After(2 * time.Second):
		t.Fatalf("fast subscriber blocked behind slow")
	}
}

// TestConcurrentReadPublish: -race coverage for concurrent reads alongside a
// publishing writer.
func TestConcurrentReadPublish(t *testing.T) {
	t.Parallel()
	s := effectivestore.New()
	s.Publish(&effectivestore.Effective{Tree: map[string]interface{}{"v": 0}})

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = s.Read()
				}
			}
		}()
	}
	for i := 0; i < 200; i++ {
		s.Publish(&effectivestore.Effective{Tree: map[string]interface{}{"v": i}})
	}
	close(stop)
	wg.Wait()
}
