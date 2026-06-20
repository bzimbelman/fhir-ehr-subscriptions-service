// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

import (
	"errors"
	"testing"
)

// OP #227: Framer.Append must enforce the pending-buffer cap itself —
// not rely on Next() noticing on the next call. The pre-fix behavior
// allowed `pending` to grow to `2*maxBody + ReadBufBytes` because the
// cap check fired only at the top of Next. After the fix, an Append
// that would push pending past `2*maxBody` rejects immediately by
// returning ErrPendingExceeded.

func TestFramer_Append_RejectsBeyondPendingCap(t *testing.T) {
	t.Parallel()

	const maxBody = 1024

	t.Run("single_append_2x_max_plus_one_errors", func(t *testing.T) {
		t.Parallel()
		f := NewFramer(maxBody)
		overflow := make([]byte, 2*maxBody+1)
		for i := range overflow {
			overflow[i] = 'a' // not a frame marker
		}
		err := f.Append(overflow)
		if err == nil {
			t.Fatalf("Append(2*maxBody+1) must return an error")
		}
		if !errors.Is(err, ErrPendingExceeded) {
			t.Errorf("Append err=%v, want errors.Is(err, ErrPendingExceeded)=true", err)
		}
	})

	t.Run("two_appends_crossing_cap_errors", func(t *testing.T) {
		t.Parallel()
		f := NewFramer(maxBody)
		// First append fills to just under 2*maxBody — accepted.
		first := make([]byte, 2*maxBody-1)
		for i := range first {
			first[i] = 'a'
		}
		if err := f.Append(first); err != nil {
			t.Fatalf("first Append: %v", err)
		}
		// Second append pushes past 2*maxBody — must be rejected.
		second := []byte{'a', 'a', 'a'}
		err := f.Append(second)
		if err == nil {
			t.Fatalf("Append crossing 2*maxBody must return an error")
		}
		if !errors.Is(err, ErrPendingExceeded) {
			t.Errorf("Append err=%v, want errors.Is(err, ErrPendingExceeded)=true", err)
		}
	})

	t.Run("under_cap_no_error", func(t *testing.T) {
		t.Parallel()
		f := NewFramer(maxBody)
		under := make([]byte, 2*maxBody-1)
		for i := range under {
			under[i] = 'b'
		}
		if err := f.Append(under); err != nil {
			t.Fatalf("Append(2*maxBody-1) must succeed; got %v", err)
		}
	})

	t.Run("zero_length_no_error", func(t *testing.T) {
		t.Parallel()
		f := NewFramer(maxBody)
		if err := f.Append(nil); err != nil {
			t.Errorf("Append(nil) must succeed; got %v", err)
		}
		if err := f.Append([]byte{}); err != nil {
			t.Errorf("Append([]byte{}) must succeed; got %v", err)
		}
	})
}
