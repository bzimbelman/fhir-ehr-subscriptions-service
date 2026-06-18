// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package scheduler_test

import (
	"math"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/scheduler"
)

// TestBackoffExponentialUnclamped: with no jitter and no retry-after
// hint, the next_attempt_at offset doubles with each attempt up to the
// cap.
func TestBackoffExponentialUnclamped(t *testing.T) {
	t.Parallel()
	cfg := scheduler.RetryConfig{
		Initial:     10 * time.Second,
		Max:         1 * time.Hour,
		Min:         1 * time.Second,
		Jitter:      0, // deterministic
		MaxAttempts: 8,
	}
	cases := []struct {
		attempt int32
		want    time.Duration
	}{
		{0, 10 * time.Second},
		{1, 20 * time.Second},
		{2, 40 * time.Second},
		{3, 80 * time.Second},
		{4, 160 * time.Second},
		{5, 320 * time.Second},
		{6, 640 * time.Second},
		{7, 1280 * time.Second},
		{20, 1 * time.Hour}, // capped
	}
	rng := scheduler.DeterministicRNG(0)
	for _, tc := range cases {
		got := scheduler.ComputeBackoff(cfg, tc.attempt, 0, rng)
		if got != tc.want {
			t.Errorf("attempt=%d: got %v want %v", tc.attempt, got, tc.want)
		}
	}
}

// TestBackoffRetryAfterWins: when the channel reports a retry-after
// hint, we honor it (clamped into [min, max]).
func TestBackoffRetryAfterWins(t *testing.T) {
	t.Parallel()
	cfg := scheduler.RetryConfig{
		Initial: 10 * time.Second, Max: time.Hour, Min: 5 * time.Second,
	}
	rng := scheduler.DeterministicRNG(0)

	if got := scheduler.ComputeBackoff(cfg, 0, 30*time.Second, rng); got != 30*time.Second {
		t.Errorf("retry-after honored: got %v", got)
	}
	// Below min → clamped up.
	if got := scheduler.ComputeBackoff(cfg, 0, 1*time.Second, rng); got != 5*time.Second {
		t.Errorf("retry-after clamp-up: got %v", got)
	}
	// Above max → clamped down.
	if got := scheduler.ComputeBackoff(cfg, 0, 7*time.Hour, rng); got != time.Hour {
		t.Errorf("retry-after clamp-down: got %v", got)
	}
}

// TestBackoffJitterBoundedSymmetric: with jitter > 0 the backoff is
// in [base*(1-j), base*(1+j)] for the configured jitter fraction.
func TestBackoffJitterBoundedSymmetric(t *testing.T) {
	t.Parallel()
	cfg := scheduler.RetryConfig{
		Initial: 10 * time.Second, Max: time.Hour, Min: 0, Jitter: 0.2,
	}
	rng := scheduler.DeterministicRNG(42)
	base := 10 * time.Second
	low := time.Duration(float64(base) * 0.8)
	high := time.Duration(float64(base) * 1.2)
	for i := 0; i < 100; i++ {
		got := scheduler.ComputeBackoff(cfg, 0, 0, rng)
		if got < low || got > high {
			t.Errorf("iteration %d: got %v outside [%v,%v]", i, got, low, high)
		}
	}
}

// TestClassifyOutcomeDelivered: Delivered outcome maps to
// Action=AdvanceAndMarkDelivered with no retry.
func TestClassifyOutcomeDelivered(t *testing.T) {
	t.Parallel()
	out := scheduler.ClassifyOutcome(deliveredOutcome(), scheduler.RetryConfig{MaxAttempts: 8}, 1)
	if out.Action != scheduler.ActionMarkDelivered {
		t.Errorf("got %v want MarkDelivered", out.Action)
	}
}

// TestClassifyOutcomeTransientUnderMax: Transient with attempts < max
// stays transient and reschedules.
func TestClassifyOutcomeTransientUnderMax(t *testing.T) {
	t.Parallel()
	out := scheduler.ClassifyOutcome(transientOutcome(), scheduler.RetryConfig{MaxAttempts: 5}, 2)
	if out.Action != scheduler.ActionRescheduleTransient {
		t.Errorf("got %v want RescheduleTransient", out.Action)
	}
}

// TestClassifyOutcomeTransientAtMaxBecomesFailed: Transient at the
// max-attempts boundary escalates to dead-letter (failed_permanent
// equivalent).
func TestClassifyOutcomeTransientAtMaxBecomesFailed(t *testing.T) {
	t.Parallel()
	out := scheduler.ClassifyOutcome(transientOutcome(), scheduler.RetryConfig{MaxAttempts: 3}, 3)
	if out.Action != scheduler.ActionDeadLetter {
		t.Errorf("got %v want DeadLetter (transient at max attempts)", out.Action)
	}
}

// TestClassifyOutcomePermanentDeadLetters: Permanent skips retry and
// goes straight to dead-letter.
func TestClassifyOutcomePermanentDeadLetters(t *testing.T) {
	t.Parallel()
	out := scheduler.ClassifyOutcome(permanentOutcome(), scheduler.RetryConfig{MaxAttempts: 8}, 1)
	if out.Action != scheduler.ActionDeadLetter {
		t.Errorf("got %v want DeadLetter", out.Action)
	}
}

// helpers — small wrappers so the test file does not depend on
// channel package's internal constructors.
func deliveredOutcome() scheduler.OutcomeFromChannel {
	return scheduler.OutcomeFromChannel{Kind: scheduler.OutcomeDelivered}
}
func transientOutcome() scheduler.OutcomeFromChannel {
	return scheduler.OutcomeFromChannel{Kind: scheduler.OutcomeTransient, Reason: "5xx"}
}
func permanentOutcome() scheduler.OutcomeFromChannel {
	return scheduler.OutcomeFromChannel{Kind: scheduler.OutcomePermanent, Reason: "404"}
}

// guard against accidental float overflow in tests above.
var _ = math.MaxFloat64
