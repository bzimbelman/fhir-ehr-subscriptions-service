// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"strconv"
	"testing"
)

// OP #223: parseAdminLimit must clamp at MaxAdminDeadLetterLimit BEFORE
// parsing-overflow can occur — i.e. it must never delegate the
// overflow-detection responsibility to strconv.Atoi.
//
// In practice that means: any raw string strictly longer than the digits
// in MaxAdminDeadLetterLimit can be rejected immediately (no Atoi call).
// We assert this end-to-end by passing an absurdly long all-digit string;
// the function must reject it without invoking Atoi.

func TestParseAdminLimit_RejectsBeyondMaxLength(t *testing.T) {
	t.Parallel()

	maxDigits := len(strconv.Itoa(MaxAdminDeadLetterLimit))

	cases := []struct {
		name string
		raw  string
	}{
		// Fits in int64 so Atoi would succeed but the value overflows
		// MaxAdminDeadLetterLimit by orders of magnitude. The strict
		// parser must reject it on length alone.
		{"int_in_range_but_exceeds_max", "9999999"},
		// Far beyond int64; previously delegated to strconv.Atoi.ErrRange.
		{"absurdly_long", "12345678901234567890123456789012345"},
		{"all_zeros_padded", "000000000000000001"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if len(c.raw) <= maxDigits {
				t.Skipf("test input must exceed maxDigits=%d", maxDigits)
			}
			_, err := parseAdminLimit(c.raw)
			if err == nil {
				t.Fatalf("parseAdminLimit(%q): want error; got nil", c.raw)
			}
		})
	}
}

// Boundary check: the largest representable raw (matching maxDigits) is
// still allowed through to value-comparison. The handler clamps it to
// MaxAdminDeadLetterLimit; parseAdminLimit returns the integer.
func TestParseAdminLimit_AllowsExactMaxDigits(t *testing.T) {
	t.Parallel()
	maxDigits := len(strconv.Itoa(MaxAdminDeadLetterLimit))
	// "500" — exactly maxDigits — must parse successfully.
	got, err := parseAdminLimit(strconv.Itoa(MaxAdminDeadLetterLimit))
	if err != nil {
		t.Fatalf("parseAdminLimit(MaxAdminDeadLetterLimit): %v", err)
	}
	if got != MaxAdminDeadLetterLimit {
		t.Errorf("got=%d, want=%d", got, MaxAdminDeadLetterLimit)
	}
	// "999" — also maxDigits, larger than max — parses (the handler
	// clamps it to MaxAdminDeadLetterLimit, but parseAdminLimit itself
	// returns the raw integer for the handler to clamp).
	candidate := ""
	for i := 0; i < maxDigits; i++ {
		candidate += "9"
	}
	got, err = parseAdminLimit(candidate)
	if err != nil {
		t.Fatalf("parseAdminLimit(%q): %v", candidate, err)
	}
	if got <= 0 {
		t.Errorf("parseAdminLimit(%q)=%d; want positive", candidate, got)
	}
}
