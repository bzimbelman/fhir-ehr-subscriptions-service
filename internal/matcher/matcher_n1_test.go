// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package matcher

import (
	"sync/atomic"
	"testing"
)

// N-1: SetBackoffReporter installs and unsets a per-worker backoff
// observer. The fast-path (nil reporter) is the legacy behavior; with
// a reporter installed, fan-out wiring can publish a Prometheus gauge.
func TestN1_SetBackoffReporterStoresAndUnsetsCallback(t *testing.T) {
	t.Parallel() // safe — atomic.Pointer is the storage primitive

	// Save and restore so other parallel tests aren't affected.
	prev := backoffReporter.Load()
	t.Cleanup(func() {
		if prev == nil {
			backoffReporter.Store(nil)
		} else {
			backoffReporter.Store(prev)
		}
	})

	var calls atomic.Int32
	SetBackoffReporter(func(seconds float64) {
		_ = seconds
		calls.Add(1)
	})
	if r := backoffReporter.Load(); r == nil {
		t.Fatalf("reporter should be installed")
	}
	(*backoffReporter.Load())(0.5)
	if calls.Load() != 1 {
		t.Fatalf("reporter not called: got %d", calls.Load())
	}

	SetBackoffReporter(nil)
	if r := backoffReporter.Load(); r != nil {
		t.Fatalf("reporter should be unset")
	}
}
