// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package resthook

import (
	"strings"
	"testing"
)

// N-1: formatTraceparent must always emit a hex-only trace-id segment
// per the W3C grammar (no '-' or other delimiters), so a non-UUID
// correlation id (e.g., a vendor ULID with letters above 'f') still
// produces a parser-valid traceparent.
func TestN1_FormatTraceparentEmitsHexOnly(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
	}{
		{"uuid", "abcdef12-3456-7890-abcd-ef0123456789"},
		{"ulid_with_uppercase", "01ARZ3NDEKTSV4RRFFQ69G5FAV"},
		{"random_garbage", "*&^%$ not-hex but-still:given"},
		{"empty", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := formatTraceparent(tc.in)
			parts := strings.Split(got, "-")
			if len(parts) != 4 {
				t.Fatalf("traceparent shape: got %q, want 4 dash-separated parts", got)
			}
			if parts[0] != "00" {
				t.Fatalf("version segment: got %q want %q", parts[0], "00")
			}
			if len(parts[1]) != 32 {
				t.Fatalf("trace-id length: got %d want 32 (input=%q)", len(parts[1]), tc.in)
			}
			for i := 0; i < len(parts[1]); i++ {
				c := parts[1][i]
				if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
					t.Fatalf("non-hex byte %q in trace-id segment %q (input=%q)", c, parts[1], tc.in)
				}
			}
			if len(parts[2]) != 16 {
				t.Fatalf("span-id length: got %d want 16", len(parts[2]))
			}
			if parts[3] != "00" {
				t.Fatalf("flags segment: got %q want %q", parts[3], "00")
			}
		})
	}
}
