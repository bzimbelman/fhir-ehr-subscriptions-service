// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package partition

import (
	"testing"
	"time"
)

func TestFirstOfMonth(t *testing.T) {
	t.Parallel()
	in := time.Date(2026, 6, 17, 12, 34, 56, 0, time.UTC)
	got := firstOfMonth(in)
	want := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestParseSuffixDate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		par  string
		want time.Time
		ok   bool
	}{
		{"resource_changes_2026_06", "resource_changes", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), true},
		{"ehr_events_2026_12", "ehr_events", time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), true},
		{"ehr_events_garbage", "ehr_events", time.Time{}, false},
		{"unrelated", "ehr_events", time.Time{}, false},
	}
	for _, c := range cases {
		got, ok := parseSuffixDate(c.in, c.par)
		if ok != c.ok {
			t.Errorf("parseSuffixDate(%q, %q) ok=%v want %v", c.in, c.par, ok, c.ok)
			continue
		}
		if ok && !got.Equal(c.want) {
			t.Errorf("got %v want %v", got, c.want)
		}
	}
}

func TestTickOnNilPoolErrors(t *testing.T) {
	t.Parallel()
	if err := Tick(t.Context(), nil, Config{}); err == nil {
		t.Error("expected error on nil pool")
	}
}
