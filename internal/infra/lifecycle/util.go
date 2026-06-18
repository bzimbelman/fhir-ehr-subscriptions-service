// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"time"
)

// withOptionalTimeout returns a context with the given nanosecond timeout
// when timeoutNanos > 0. When timeoutNanos <= 0, it returns a cancellable
// derivative of parent so the caller can defer cancel() unconditionally.
//
// Centralizing the helper means the readiness aggregator and the shutdown
// sequencer derive deadlines the same way.
func withOptionalTimeout(parent context.Context, timeoutNanos int64) (context.Context, context.CancelFunc) {
	if timeoutNanos <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, time.Duration(timeoutNanos))
}

// withDeadline returns a context that fires no later than the given
// absolute deadline. Used by the shutdown sequencer to cap each phase at
// its phase deadline (which is always <= the total grace deadline).
func withDeadline(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	return context.WithDeadline(parent, deadline)
}
