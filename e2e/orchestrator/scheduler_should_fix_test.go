// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/scheduler"
)

// TestE2E_S8_4_PerChannelMaxAttempts — wired-up coverage of the
// per-channel MaxAttempts override (S-8.4). Routes a transient
// outcome through ClassifyOutcomeForChannel with a low override and
// asserts dead-letter at attempt N where N == per-channel cap.
func TestE2E_S8_4_PerChannelMaxAttempts(t *testing.T) {
	cfg := scheduler.RetryConfig{
		Initial:     time.Second,
		Max:         time.Hour,
		MaxAttempts: 8,
		PerChannel: map[string]int32{
			"rest-hook": 2,
		},
	}
	transient := scheduler.OutcomeFromChannel{Kind: scheduler.OutcomeTransient, Reason: "5xx"}

	d := scheduler.ClassifyOutcomeForChannel(transient, cfg, "rest-hook", 1)
	if d.Action != scheduler.ActionRescheduleTransient {
		t.Errorf("rest-hook attempt=1 want Reschedule got %v", d.Action)
	}
	d = scheduler.ClassifyOutcomeForChannel(transient, cfg, "rest-hook", 2)
	if d.Action != scheduler.ActionDeadLetter {
		t.Errorf("rest-hook attempt=2 want DeadLetter (per-channel cap) got %v", d.Action)
	}
	d = scheduler.ClassifyOutcomeForChannel(transient, cfg, "message", 2)
	if d.Action != scheduler.ActionRescheduleTransient {
		t.Errorf("message attempt=2 want Reschedule (global cap=8) got %v", d.Action)
	}
}

// TestE2E_S8_2_NotFoundIsPermanent — subscription-deleted / event-missing
// route to dead-letter, not transient. Covers the worker bail-out path
// classification.
func TestE2E_S8_2_NotFoundIsPermanent(t *testing.T) {
	cfg := scheduler.RetryConfig{MaxAttempts: 8}
	d := scheduler.ClassifyRequeueReason(scheduler.ReasonSubscriptionUnavailable, cfg, 1)
	if d.Action != scheduler.ActionDeadLetter {
		t.Errorf("subscription_unavailable wantDeadLetter got %v", d.Action)
	}
	d = scheduler.ClassifyRequeueReason(scheduler.ReasonEhrEventUnavailable, cfg, 1)
	if d.Action != scheduler.ActionDeadLetter {
		t.Errorf("ehr_event_unavailable wantDeadLetter got %v", d.Action)
	}
	d = scheduler.ClassifyRequeueReason("channel_unavailable: rest-hook", cfg, 1)
	if d.Action != scheduler.ActionRescheduleTransient {
		t.Errorf("channel_unavailable wantReschedule got %v", d.Action)
	}
}

// TestE2E_S8_5_JitterClamped — jitter > 0.5 is clamped at applyDefaults
// so the multiplier (1+offset) cannot approach zero.
func TestE2E_S8_5_JitterClamped(t *testing.T) {
	cfg := scheduler.RetryConfig{
		Initial:     10 * time.Second,
		Max:         time.Hour,
		Min:         100 * time.Millisecond,
		Jitter:      0.99, // intentionally over the cap
		MaxAttempts: 8,
	}
	rng := scheduler.DeterministicRNG(0)
	base := 10 * time.Second
	low := time.Duration(float64(base) * 0.5)
	high := time.Duration(float64(base) * 1.5)
	for i := 0; i < 200; i++ {
		got := scheduler.ComputeBackoff(cfg, 0, 0, rng)
		if got < low || got > high {
			t.Errorf("iter %d: got %v outside clamped [%v, %v]", i, got, low, high)
		}
	}
}
