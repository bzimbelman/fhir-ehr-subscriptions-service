// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"sync/atomic"
	"testing"
)

// N-1: equalJSON must NOT swallow unmarshal errors and treat malformed
// JSON as equal to anything that round-trips through Marshal(nil).
func TestN1_EqualJSONMalformedFallsBackToBytes(t *testing.T) {
	t.Parallel()

	// One side parses, the other does not. Pre-N-1 both Marshal(nil)'d
	// to "null" and the function returned true. Now we fall back to
	// bytes.Equal so distinct payloads compare false.
	if equalJSON([]byte(`{"a":1}`), []byte(`not-json`)) {
		t.Fatalf("expected false: malformed RHS must not be equal to a parsable LHS")
	}
	if equalJSON([]byte(`not-json`), []byte(`{"a":1}`)) {
		t.Fatalf("expected false: malformed LHS must not be equal to a parsable RHS")
	}

	// Two malformed but byte-different inputs must be unequal.
	if equalJSON([]byte(`<html>`), []byte(`<other>`)) {
		t.Fatalf("expected false for two distinct malformed inputs")
	}

	// Two byte-identical malformed inputs may compare equal — they
	// represent the same blob even if neither side parses.
	if !equalJSON([]byte(`{a`), []byte(`{a`)) {
		t.Fatalf("byte-identical malformed inputs should compare equal")
	}

	// Empty / empty equal.
	if !equalJSON(nil, nil) {
		t.Fatalf("nil/nil must be equal")
	}

	// Reordered keys still equal (canonical-form round trip works on
	// well-formed inputs).
	if !equalJSON([]byte(`{"a":1,"b":2}`), []byte(`{"b":2,"a":1}`)) {
		t.Fatalf("reordered keys should compare equal")
	}
}

// fakeRandFailureRecorder counts calls. Implements both
// MetricsRecorder (minimum) and the optional RandFailureRecorder.
type fakeRandFailureRecorder struct {
	rand atomic.Int64
}

func (f *fakeRandFailureRecorder) RecordSubscriptionCreated()       {}
func (f *fakeRandFailureRecorder) RecordSubscriptionUpdated()       {}
func (f *fakeRandFailureRecorder) RecordSubscriptionDeleted()       {}
func (f *fakeRandFailureRecorder) RecordWSBindingTokenIssued()      {}
func (f *fakeRandFailureRecorder) RecordValidationFailure(_ string) {}
func (f *fakeRandFailureRecorder) RecordRandFailure()               { f.rand.Add(1) }

var _ MetricsRecorder = (*fakeRandFailureRecorder)(nil)
var _ RandFailureRecorder = (*fakeRandFailureRecorder)(nil)

// N-1: A recorder implementing RandFailureRecorder is invoked through
// the optional type-assertion path on (*server).recordRandFailure.
func TestN1_RecordRandFailureInvokesOptionalRecorder(t *testing.T) {
	t.Parallel()
	rec := &fakeRandFailureRecorder{}
	s := &server{deps: Deps{Metrics: rec}}
	s.recordRandFailure()
	s.recordRandFailure()
	if got := rec.rand.Load(); got != 2 {
		t.Fatalf("RecordRandFailure called %d times; want 2", got)
	}
}

// N-1: Without RandFailureRecorder, recordRandFailure is a safe no-op.
type minimalRecorder struct{}

func (minimalRecorder) RecordSubscriptionCreated()       {}
func (minimalRecorder) RecordSubscriptionUpdated()       {}
func (minimalRecorder) RecordSubscriptionDeleted()       {}
func (minimalRecorder) RecordWSBindingTokenIssued()      {}
func (minimalRecorder) RecordValidationFailure(_ string) {}

func TestN1_RecordRandFailureNoOpWithoutOptionalRecorder(t *testing.T) {
	t.Parallel()
	s := &server{deps: Deps{Metrics: minimalRecorder{}}}
	s.recordRandFailure() // must not panic
}

// And without a Metrics dep at all.
func TestN1_RecordRandFailureNoOpWithoutMetrics(t *testing.T) {
	t.Parallel()
	s := &server{deps: Deps{}}
	s.recordRandFailure() // must not panic
}
