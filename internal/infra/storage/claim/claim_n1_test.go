// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package claim

import "testing"

// N-1: hasSkipLocked must ignore SQL inside line comments, block
// comments, and string literals so a comment claiming to have the
// clause cannot trick the runtime check.
func TestN1_HasSkipLockedIgnoresCommentsAndStrings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		sql  string
		want bool
	}{
		{"line_comment_only", "-- FOR UPDATE SKIP LOCKED\nSELECT 1", false},
		{"block_comment_only", "/* FOR UPDATE SKIP LOCKED */ SELECT 1", false},
		{"string_only", "SELECT 'FOR UPDATE SKIP LOCKED' FROM t", false},
		{"missing_skip_locked", "SELECT 1 FROM t FOR UPDATE", false},
		{"missing_for_update", "SELECT 1 FROM t SKIP LOCKED", false},
		{"valid_with_unrelated_comment", "-- header\nSELECT 1 FROM t FOR UPDATE SKIP LOCKED", true},
		{"valid_with_unrelated_string", "SELECT 'note' FROM t FOR UPDATE SKIP LOCKED", true},
		{"valid_lower_case", "SELECT 1 FROM t for update skip locked", true},
		{"valid_with_block_comment_around", "/* claim */ SELECT 1 FROM t FOR UPDATE SKIP LOCKED -- ok", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := hasSkipLocked(tc.sql); got != tc.want {
				t.Fatalf("sql=%q got=%v want=%v", tc.sql, got, tc.want)
			}
		})
	}
}
