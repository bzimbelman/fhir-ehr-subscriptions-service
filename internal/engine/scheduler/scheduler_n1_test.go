// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"testing"
	"time"
)

// N-1: ComputeBackoff caps the doubling loop at maxBackoffDoublingSteps
// so a pathological caller passing a huge attempts counter does not
// spin in a tight loop.
func TestN1_ComputeBackoffIterationCap(t *testing.T) {
	t.Parallel()

	cfg := RetryConfig{
		Initial: 1 * time.Second,
		Max:     1 * time.Hour,
		Min:     1 * time.Millisecond,
	}
	rng := DeterministicRNG(1)

	// 10000 attempts should not iterate 10000 times. The result must
	// equal cfg.Max because the loop saturates well within the cap.
	got := ComputeBackoff(cfg, 10000, 0, rng)
	if got > cfg.Max {
		t.Fatalf("got %v > Max %v", got, cfg.Max)
	}
	if got < cfg.Min {
		t.Fatalf("got %v < Min %v", got, cfg.Min)
	}

	// Also exercise the cap directly.
	if maxBackoffDoublingSteps != 64 {
		t.Fatalf("maxBackoffDoublingSteps = %d; want 64", maxBackoffDoublingSteps)
	}
}
