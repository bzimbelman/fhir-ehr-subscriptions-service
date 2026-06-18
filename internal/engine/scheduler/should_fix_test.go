// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// SHOULD-FIX coverage for S-8 audit findings.
// Tests assert classification semantics, jitter bounds, and configuration
// surfaces independent of an actual database.

package scheduler_test

import (
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/scheduler"
)

// TestS8_5_JitterClampedToHalf — S-8.5: invalid jitter is clamped at
// applyDefaults so a misconfigured cfg cannot produce a negative
// multiplier (1 + offset < 0). The validator only allowed [0, 1] which
// is too generous; we tighten to [0, 0.5] so the backoff cannot be
// flipped negative even in pathological cases.
func TestS8_5_JitterClampedToHalf(t *testing.T) {
	t.Parallel()
	// Jitter > 0.5 is clamped down. Any RNG with Float64 returning 1.0
	// would otherwise give base * (1 + jitter), which is fine, but
	// Float64 returning 0.0 gives base * (1 - jitter); a jitter of
	// 0.99 puts the floor at base*0.01 which floors to Min. We assert
	// the *cfg* is clamped after applyDefaults — readable through
	// ComputeBackoff staying within [base*0.5, base*1.5] for any RNG.
	cfg := scheduler.RetryConfig{
		Initial:     10 * time.Second,
		Max:         time.Hour,
		Min:         100 * time.Millisecond,
		Jitter:      0.99, // intentionally too high
		MaxAttempts: 8,
	}
	rng := scheduler.DeterministicRNG(0)
	base := 10 * time.Second
	low := time.Duration(float64(base) * 0.5)
	high := time.Duration(float64(base) * 1.5)
	for i := 0; i < 200; i++ {
		got := scheduler.ComputeBackoff(cfg, 0, 0, rng)
		if got < low || got > high {
			t.Errorf("iteration %d: got %v outside clamped range [%v,%v]", i, got, low, high)
		}
	}
}

// TestS8_4_PerChannelMaxAttempts — S-8.4: MaxAttempts can be overridden
// per channel-type so a slow rest-hook subscriber and a fast email
// channel can have different escalation curves without changing the
// global retry config.
func TestS8_4_PerChannelMaxAttempts(t *testing.T) {
	t.Parallel()
	cfg := scheduler.RetryConfig{
		Initial:     10 * time.Second,
		Max:         time.Hour,
		MaxAttempts: 8,
		PerChannel: map[string]int32{
			"rest-hook": 3,
		},
	}
	// rest-hook hits its (smaller) cap at attempt 3.
	out := scheduler.ClassifyOutcomeForChannel(transientOutcome(), cfg, "rest-hook", 3)
	if out.Action != scheduler.ActionDeadLetter {
		t.Errorf("rest-hook attempt=3 wantDeadLetter got %v", out.Action)
	}
	// message uses the global 8.
	out = scheduler.ClassifyOutcomeForChannel(transientOutcome(), cfg, "message", 3)
	if out.Action != scheduler.ActionRescheduleTransient {
		t.Errorf("message attempt=3 wantReschedule got %v", out.Action)
	}
}

// TestS8_2_NotFoundIsPermanent — S-8.2: a "row missing" path (subscription
// or ehr_event nil) is permanent, not transient: the row will never come
// back so the retry budget should not be burned.
func TestS8_2_NotFoundIsPermanent(t *testing.T) {
	t.Parallel()
	d := scheduler.ClassifyRequeueReason("subscription_unavailable", scheduler.RetryConfig{MaxAttempts: 8}, 1)
	if d.Action != scheduler.ActionDeadLetter {
		t.Errorf("subscription_unavailable wantDeadLetter got %v", d.Action)
	}
	d = scheduler.ClassifyRequeueReason("ehr_event_unavailable", scheduler.RetryConfig{MaxAttempts: 8}, 1)
	if d.Action != scheduler.ActionDeadLetter {
		t.Errorf("ehr_event_unavailable wantDeadLetter got %v", d.Action)
	}
	// channel_unavailable IS transient — operator may register the
	// channel later; do not burn the row.
	d = scheduler.ClassifyRequeueReason("channel_unavailable: rest-hook", scheduler.RetryConfig{MaxAttempts: 8}, 1)
	if d.Action != scheduler.ActionRescheduleTransient {
		t.Errorf("channel_unavailable wantReschedule got %v", d.Action)
	}
}

// TestS8_3_BuildErrorPermanent — S-8.3: a deterministic build error
// (missing topic, bad shape) is permanent; transient build conditions
// keep the audit-doc default of "transient".
func TestS8_3_BuildErrorPermanent(t *testing.T) {
	t.Parallel()
	// Anything tagged as "permanent" routes to dead-letter immediately.
	d := scheduler.ClassifyBuildError(scheduler.BuildErrorPermanent, scheduler.RetryConfig{MaxAttempts: 8}, 1)
	if d.Action != scheduler.ActionDeadLetter {
		t.Errorf("BuildErrorPermanent wantDeadLetter got %v", d.Action)
	}
	d = scheduler.ClassifyBuildError(scheduler.BuildErrorTransient, scheduler.RetryConfig{MaxAttempts: 8}, 1)
	if d.Action != scheduler.ActionRescheduleTransient {
		t.Errorf("BuildErrorTransient wantReschedule got %v", d.Action)
	}
}
