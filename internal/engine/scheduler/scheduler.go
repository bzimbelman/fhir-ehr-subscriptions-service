// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package scheduler is the Stage 5 driver: it claims pending
// deliveries rows, builds the notification Bundle, dispatches to a
// channel, and applies the channel's DeliveryOutcome to the row plus
// the subscription state. It owns the retry curve, the per-row
// attempts/next_attempt_at advance, and the dead-letter routing.
//
// The scheduler does NOT own:
//   - per-subscription filterBy / topic match (Stage 3 / submatcher).
//   - Bundle assembly (Stage 4 / builder).
//   - protocol bytes on the wire (the channel module).
//
// Concurrency: each ticker iteration is one transaction that claims a
// batch under SELECT FOR UPDATE SKIP LOCKED, dispatches to the channel
// outside the transaction (so a slow channel does not hold DB rows
// open), and writes the outcome inside a fresh transaction. This is
// the same shape the matcher worker uses.
//
// The schema's deliveries.status enum is the v0 set
// {pending, delivering, delivered, failed, dead}. The LLD speaks
// {failed_transient, failed_permanent} but we honor the schema and map
// transient → "pending" (with attempts++ and next_attempt_at advanced)
// or "failed" (when max_attempts is exhausted, escalating to dead),
// and permanent → "dead". The dead_letters table is populated on
// DeadLetter outcomes.
package scheduler

import (
	"math/rand/v2"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
)

// RetryConfig governs the backoff curve and the dead-letter
// escalation. Defaults match LLD §"Configuration knobs":
//
//	initial 10s, max 1h, min 1s, jitter 0.2, max_attempts 8.
type RetryConfig struct {
	Initial     time.Duration
	Max         time.Duration
	Min         time.Duration
	Jitter      float64 // [0,1] — symmetric ± fraction of base
	MaxAttempts int32
}

// applyDefaults zero-fills with the LLD defaults.
func (c *RetryConfig) applyDefaults() {
	if c.Initial == 0 {
		c.Initial = 10 * time.Second
	}
	if c.Max == 0 {
		c.Max = time.Hour
	}
	if c.Min == 0 {
		c.Min = time.Second
	}
	if c.MaxAttempts == 0 {
		c.MaxAttempts = 8
	}
	if c.Jitter < 0 {
		c.Jitter = 0
	}
	if c.Jitter > 1 {
		c.Jitter = 1
	}
}

// RNG is the random source the backoff curve consumes. *rand.Rand is
// the canonical implementation; tests inject a deterministic source
// via DeterministicRNG.
type RNG interface {
	Float64() float64
}

// DeterministicRNG returns an RNG seeded with the given value, useful
// for tests that need reproducible jitter.
//
// Jitter is not security-relevant; math/rand/v2 PCG is intentional
// here. crypto/rand would consume entropy for no benefit.
//
//nolint:gosec // G404 / G115: jitter RNG is non-security; seed mixing is intentional.
func DeterministicRNG(seed int64) RNG {
	s := uint64(seed)
	return rand.New(rand.NewPCG(s, s^0x9E3779B97F4A7C15))
}

// ComputeBackoff returns the duration to wait before the next attempt
// for a delivery whose current attempts counter is `attempts` (i.e.,
// 0 for the first scheduled retry). The retryAfter hint, if non-zero,
// wins (clamped into [min, max]). Otherwise the curve is
// initial * 2^attempts, capped at max, with symmetric jitter.
func ComputeBackoff(cfg RetryConfig, attempts int32, retryAfter time.Duration, rng RNG) time.Duration {
	cfg.applyDefaults()
	if rng == nil {
		//nolint:gosec // G115: UnixNano is monotonic positive; truncation is intentional.
		seed := uint64(time.Now().UnixNano())
		//nolint:gosec // G404: jitter RNG is non-security; PCG is fine.
		rng = rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
	}
	if retryAfter > 0 {
		return clamp(retryAfter, cfg.Min, cfg.Max)
	}
	base := cfg.Initial
	for i := int32(0); i < attempts; i++ {
		next := base * 2
		if next < base || next > cfg.Max {
			base = cfg.Max
			break
		}
		base = next
	}
	if base > cfg.Max {
		base = cfg.Max
	}
	if cfg.Jitter > 0 {
		// Map [0,1) to [-jitter, +jitter].
		offset := (rng.Float64()*2 - 1) * cfg.Jitter
		base = time.Duration(float64(base) * (1 + offset))
	}
	return clamp(base, cfg.Min, cfg.Max)
}

func clamp(d, low, high time.Duration) time.Duration {
	if d < low {
		return low
	}
	if d > high {
		return high
	}
	return d
}

// OutcomeKind mirrors channel.OutcomeKind in a scheduler-local type so
// tests do not have to depend on the channel package's iota ordering.
type OutcomeKind int

// OutcomeKind values.
const (
	// OutcomeDelivered: the subscriber accepted the bundle.
	OutcomeDelivered OutcomeKind = iota
	// OutcomeTransient: retryable failure.
	OutcomeTransient
	// OutcomePermanent: terminal failure for this delivery.
	OutcomePermanent
)

// OutcomeFromChannel is the scheduler's view of one channel call's
// result. Mapped from channel.DeliveryOutcome.
type OutcomeFromChannel struct {
	Kind       OutcomeKind
	Reason     string
	RetryAfter time.Duration
	StatusCode int
}

// FromChannelOutcome converts a channel.DeliveryOutcome into the
// scheduler-local form.
func FromChannelOutcome(o channel.DeliveryOutcome) OutcomeFromChannel {
	switch o.Kind {
	case channel.OutcomeDelivered:
		return OutcomeFromChannel{Kind: OutcomeDelivered, StatusCode: o.StatusCode}
	case channel.OutcomeTransient:
		return OutcomeFromChannel{Kind: OutcomeTransient, Reason: o.Reason, RetryAfter: o.RetryAfter, StatusCode: o.StatusCode}
	case channel.OutcomePermanent:
		return OutcomeFromChannel{Kind: OutcomePermanent, Reason: o.Reason, StatusCode: o.StatusCode}
	default:
		// Defensive: any unknown OutcomeKind is treated as a permanent
		// failure rather than silently retried — surfaces channel bugs.
		return OutcomeFromChannel{Kind: OutcomePermanent, Reason: "unknown outcome kind"}
	}
}

// Action is the scheduler's instruction set for what to do with the
// claimed deliveries row after a channel call returns.
type Action int

// Action values.
const (
	// ActionMarkDelivered: row → 'delivered', cursor advanced.
	ActionMarkDelivered Action = iota
	// ActionRescheduleTransient: row → 'pending', attempts++,
	// next_attempt_at = now + ComputeBackoff(...).
	ActionRescheduleTransient
	// ActionDeadLetter: row → 'dead', insert dead_letters row.
	ActionDeadLetter
)

// String renders the enum for log + test diagnostics.
func (a Action) String() string {
	switch a {
	case ActionMarkDelivered:
		return "MarkDelivered"
	case ActionRescheduleTransient:
		return "RescheduleTransient"
	case ActionDeadLetter:
		return "DeadLetter"
	default:
		return "Unknown"
	}
}

// Decision is the per-row dispatch outcome.
type Decision struct {
	Action Action
	Reason string // populated on DeadLetter / RescheduleTransient
}

// ClassifyOutcome maps a channel outcome onto a scheduler decision.
// `attempts` is the post-attempt counter (i.e., the deliveries row's
// attempts column AFTER the current attempt has been counted, i.e.
// row.attempts + 1 in the caller).
func ClassifyOutcome(o OutcomeFromChannel, retry RetryConfig, attempts int32) Decision {
	retry.applyDefaults()
	switch o.Kind {
	case OutcomeDelivered:
		return Decision{Action: ActionMarkDelivered}
	case OutcomePermanent:
		return Decision{Action: ActionDeadLetter, Reason: "permanent: " + o.Reason}
	case OutcomeTransient:
		if attempts >= retry.MaxAttempts {
			return Decision{Action: ActionDeadLetter, Reason: "max_attempts_exhausted: " + o.Reason}
		}
		return Decision{Action: ActionRescheduleTransient, Reason: o.Reason}
	default:
		return Decision{Action: ActionDeadLetter, Reason: "unknown outcome"}
	}
}
