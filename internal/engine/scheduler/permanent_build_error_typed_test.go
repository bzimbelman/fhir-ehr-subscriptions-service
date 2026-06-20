// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"errors"
	"fmt"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/builder"
)

// OP #222: replace the brittle string-matching with a typed sentinel.
// builder.ErrPermanent must be satisfied via errors.Is for every
// deterministic build error; the scheduler then classifies as permanent
// without inspecting err.Error().
func TestIsPermanentBuildError_TypedSentinel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated", errors.New("network is unreachable"), false},
		{"sentinel_wrapped", fmt.Errorf("builder: subscription has nil id: %w", builder.ErrPermanent), true},
		{"sentinel_direct", builder.ErrPermanent, true},
		// Backwards-compat: the legacy substrings still match so
		// historical errors (logged or persisted with the old format)
		// still classify correctly during the migration.
		{"legacy_nil_id", errors.New("builder: subscription has nil id"), true},
		{"legacy_decode_focus", errors.New("builder: decode focus resource: unexpected token"), true},
		{"legacy_marshal_status", errors.New("builder: marshal status: invalid"), true},
		{"legacy_marshal_bundle", errors.New("builder: marshal bundle: invalid"), true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isPermanentBuildError(tc.err); got != tc.want {
				t.Errorf("isPermanentBuildError(%v)=%v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// AC: Builder code MUST wrap errors with the typed sentinel.
// This test verifies the shape — that builder.ErrPermanent is a non-nil
// sentinel and that errors.Is correctly identifies wrapped descendants.
func TestBuilderErrPermanent_IsExportedSentinel(t *testing.T) {
	t.Parallel()
	if builder.ErrPermanent == nil {
		t.Fatal("builder.ErrPermanent must be a non-nil sentinel")
	}
	wrapped := fmt.Errorf("custom wrap: %w", builder.ErrPermanent)
	if !errors.Is(wrapped, builder.ErrPermanent) {
		t.Fatal("errors.Is must traverse %w into builder.ErrPermanent")
	}
}
