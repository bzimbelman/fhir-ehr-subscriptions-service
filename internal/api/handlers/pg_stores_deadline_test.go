// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
)

// S-2.14: storage-shaped per-query timeout knobs.
//
// The acceptance criteria require a configurable QueryTimeouts struct
// with documented defaults (5s read / 10s write) and a typed
// ErrQueryTimeout that callers can detect via errors.Is.
func TestQueryTimeouts_Defaults(t *testing.T) {
	t.Parallel()
	var qt handlers.QueryTimeouts
	qt.ApplyDefaults()
	if qt.Read != 5*time.Second {
		t.Errorf("Read default = %v, want 5s", qt.Read)
	}
	if qt.Write != 10*time.Second {
		t.Errorf("Write default = %v, want 10s", qt.Write)
	}
}

// ApplyDefaults must not stomp non-zero operator-supplied values.
func TestQueryTimeouts_ApplyDefaults_PreservesNonZero(t *testing.T) {
	t.Parallel()
	qt := handlers.QueryTimeouts{Read: 250 * time.Millisecond, Write: 750 * time.Millisecond}
	qt.ApplyDefaults()
	if qt.Read != 250*time.Millisecond || qt.Write != 750*time.Millisecond {
		t.Errorf("ApplyDefaults clobbered operator values: %+v", qt)
	}
}

// ErrQueryTimeout must be a sentinel error so callers can detect it via
// errors.Is and translate to a 504 / typed code.
func TestErrQueryTimeout_IsSentinel(t *testing.T) {
	t.Parallel()
	if handlers.ErrQueryTimeout == nil {
		t.Fatal("ErrQueryTimeout must be a non-nil sentinel")
	}
	wrapped := handlers.WrapQueryTimeout(handlers.ErrQueryTimeout, "scope")
	if !errors.Is(wrapped, handlers.ErrQueryTimeout) {
		t.Error("wrapped error must satisfy errors.Is(err, ErrQueryTimeout)")
	}
}

// translateQueryErr is the small helper used by every pg_stores query
// site. It translates the inner-context's DeadlineExceeded into the
// typed ErrQueryTimeout WHEN AND ONLY WHEN the parent context still has
// budget — that distinction matters because a caller-canceled request
// should return the parent context error, not a "query timed out"
// surface that operators would interpret as a database problem.
func TestTranslateQueryErr_DeadlineExceededYieldsTyped(t *testing.T) {
	t.Parallel()

	parent := context.Background()
	innerCtx, cancel := context.WithTimeout(parent, 1*time.Millisecond)
	defer cancel()
	// Force the deadline to fire so innerCtx.Err() is DeadlineExceeded.
	<-innerCtx.Done()

	got := handlers.TranslateQueryErr(parent, innerCtx, context.DeadlineExceeded, "list-by-client")
	if !errors.Is(got, handlers.ErrQueryTimeout) {
		t.Fatalf("expected ErrQueryTimeout, got %v (%T)", got, got)
	}
}

// When the parent context is canceled, callers should see the parent's
// cancellation, NOT a typed query-timeout error.
func TestTranslateQueryErr_ParentCancelledKeepsParentError(t *testing.T) {
	t.Parallel()

	parent, cancelParent := context.WithCancel(context.Background())
	innerCtx, cancelInner := context.WithTimeout(parent, 50*time.Millisecond)
	defer cancelInner()

	cancelParent()
	<-parent.Done()

	got := handlers.TranslateQueryErr(parent, innerCtx, context.Canceled, "list-by-client")
	if errors.Is(got, handlers.ErrQueryTimeout) {
		t.Fatalf("must NOT translate caller cancellation into ErrQueryTimeout, got %v", got)
	}
	if !errors.Is(got, context.Canceled) {
		t.Errorf("expected context.Canceled to survive translation, got %v", got)
	}
}

// Non-deadline errors must pass through unchanged (typed-wrapped is
// fine, but errors.Is(ErrQueryTimeout) must be false).
func TestTranslateQueryErr_NonDeadlineErrorsPassThrough(t *testing.T) {
	t.Parallel()

	parent := context.Background()
	innerCtx, cancel := context.WithCancel(parent)
	defer cancel()

	boom := errors.New("connection reset by peer")
	got := handlers.TranslateQueryErr(parent, innerCtx, boom, "scope")
	if errors.Is(got, handlers.ErrQueryTimeout) {
		t.Errorf("non-deadline error must not become ErrQueryTimeout: %v", got)
	}
	if !errors.Is(got, boom) {
		t.Errorf("expected wrapped to satisfy errors.Is(boom): %v", got)
	}
}
