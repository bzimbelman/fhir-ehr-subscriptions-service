// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

import (
	"sync/atomic"

	"github.com/google/uuid"
)

// connectionState collects the per-connection mutable state called out by
// LLD §4. One instance lives for the life of one TCP connection. The
// fields are atomic-int wrappers so the inflight gate (LLD §5.6) and the
// inflight gauge (LLD §7) can read/write concurrently with future fan-out
// implementations; today's serial read loop touches them from a single
// goroutine, but the contract is "safe under concurrent access" so we
// don't have to revisit when fan-out lands.
type connectionState struct {
	id               uuid.UUID
	endpointName     string
	peerAddr         string
	inflight         int32 // current inflight persist count
	consecutiveFails int32 // consecutive persist failures (reset on success)
}

// newConnectionState returns a fresh connectionState with a random
// connection id (UUID).
func newConnectionState(endpointName, peerAddr string) *connectionState {
	return &connectionState{
		id:           uuid.New(),
		endpointName: endpointName,
		peerAddr:     peerAddr,
	}
}

// inflightCount returns the current inflight count.
func (s *connectionState) inflightCount() int32 {
	return atomic.LoadInt32(&s.inflight)
}

// incInflight increments and returns the new inflight count.
func (s *connectionState) incInflight() int32 {
	return atomic.AddInt32(&s.inflight, 1)
}

// decInflight decrements and returns the new inflight count.
func (s *connectionState) decInflight() int32 {
	return atomic.AddInt32(&s.inflight, -1)
}

// markInflightForTest pre-increments inflight without doing actual work.
// Tests use it to simulate the contention case (cap reached). Production
// code MUST go through incInflight / decInflight in pairs.
func (s *connectionState) markInflightForTest() {
	atomic.AddInt32(&s.inflight, 1)
}

// recordPersistResult updates the consecutive-failure counter. err==nil
// resets to zero; non-nil increments and returns the new count.
func (s *connectionState) recordPersistResult(err error) int32 {
	if err == nil {
		atomic.StoreInt32(&s.consecutiveFails, 0)
		return 0
	}
	return atomic.AddInt32(&s.consecutiveFails, 1)
}

// consecutiveFailures returns the current consecutive-failure count.
func (s *connectionState) consecutiveFailures() int32 {
	return atomic.LoadInt32(&s.consecutiveFails)
}
