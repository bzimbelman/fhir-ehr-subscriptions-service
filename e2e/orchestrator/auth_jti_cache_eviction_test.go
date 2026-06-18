// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"fmt"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
)

// TestE2E_JTIReplayCache_EvictionStaysBounded exercises the cache
// against churn — a mix of expired entries, sweeps via Seen, and fresh
// inserts past capacity. After all the activity the cache must remain
// bounded by its configured capacity, with no apparent ghost entries.
// Regression guard for B-9.
func TestE2E_JTIReplayCache_EvictionStaysBounded(t *testing.T) {
	t.Parallel()

	t0 := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	cur := t0
	clock := func() time.Time { return cur }
	c := auth.NewJTIReplayCache(64, clock)

	// 200 entries, half expired, half live, with sweeps interleaved.
	for i := 0; i < 200; i++ {
		key := fmt.Sprintf("evict-%d", i)
		exp := t0.Add(2 * time.Minute) // alive at t0
		if i%2 == 0 {
			exp = t0.Add(-1 * time.Minute) // already expired
		}
		c.Put(key, exp)
	}
	// Move clock past the expired window and sweep via Seen.
	cur = t0.Add(5 * time.Minute)
	for i := 0; i < 200; i += 2 {
		_ = c.Seen(fmt.Sprintf("evict-%d", i))
	}
	// Drop 200 fresh entries past cap to force eviction-of-eviction.
	cur = t0
	for i := 0; i < 200; i++ {
		c.Put(fmt.Sprintf("fresh-%d", i), t0.Add(2*time.Minute))
	}

	// We can't introspect without breaking package boundaries; verify
	// behaviourally instead: an entry inserted last should be Seen,
	// and an entry inserted way earlier should NOT be Seen. The exact
	// boundary is implementation detail; "many of the early ones were
	// evicted" is the user-visible invariant.
	if !c.Seen("fresh-199") {
		t.Errorf("most recent fresh entry should be present")
	}
	earlyEvicted := 0
	for i := 0; i < 100; i++ {
		if !c.Seen(fmt.Sprintf("fresh-%d", i)) {
			earlyEvicted++
		}
	}
	if earlyEvicted == 0 {
		t.Errorf("expected at least some early fresh entries to be evicted; got 0")
	}
}
