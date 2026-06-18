// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package migrate

import "testing"

// TestDetectConcurrentRequiresExplicitDirective pins audit B-33: the
// previous heuristic was strings.Contains("CREATE INDEX CONCURRENTLY"),
// which a SQL comment would trip. The fix is an opt-in directive on
// the leading content line.
func TestDetectConcurrentRequiresExplicitDirective(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "directive on first content line",
			body: "-- @CONCURRENT\nCREATE INDEX CONCURRENTLY x ON y(z);\n",
			want: true,
		},
		{
			name: "directive after blank lines",
			body: "\n\n-- @CONCURRENT\nCREATE INDEX CONCURRENTLY x ON y(z);\n",
			want: true,
		},
		{
			name: "directive case insensitive",
			body: "-- @concurrent\nCREATE INDEX CONCURRENTLY x ON y(z);\n",
			want: true,
		},
		{
			name: "comment mentions phrase but no directive",
			body: "-- This migration would have used CREATE INDEX CONCURRENTLY but does not\nALTER TABLE x ADD COLUMN y INT;\n",
			want: false,
		},
		{
			name: "transactional migration",
			body: "ALTER TABLE pending_pairs ADD COLUMN key_version int NOT NULL DEFAULT 1;\n",
			want: false,
		},
		{
			name: "string literal contains phrase",
			body: "INSERT INTO doc (note) VALUES ('CREATE INDEX CONCURRENTLY is the preferred approach');\n",
			want: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := detectConcurrent(tc.body)
			if got != tc.want {
				t.Fatalf("detectConcurrent(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}
