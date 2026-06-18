// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// SHOULD-FIX coverage for S-10 audit findings.

package matcher

import (
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/topics/catalog"
)

// TestS10_4_ParseFlexibleDateMarksImputedUTC — S-10.4: parseFlexibleDate
// silently treats non-RFC3339 dates as UTC; the new variant returns a
// boolean flag so callers can metric / decide.
func TestS10_4_ParseFlexibleDateMarksImputedUTC(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in        string
		ok        bool
		imputedTZ bool
	}{
		{"2026-01-01T00:00:00Z", true, false},
		{"2026-01-01T00:00:00+05:00", true, false},
		{"2026-01-01T12:34:56", true, true},
		{"2026-01-01", true, true},
		{"not-a-date", false, false},
	}
	for _, tc := range cases {
		_, imputed, ok := parseFlexibleDateWithFlag(tc.in)
		if ok != tc.ok {
			t.Errorf("%q ok got %v want %v", tc.in, ok, tc.ok)
		}
		if ok && imputed != tc.imputedTZ {
			t.Errorf("%q imputedTZ got %v want %v", tc.in, imputed, tc.imputedTZ)
		}
	}
}

// TestS10_6_WorkerMaxRowAttempts — S-10.6: poison row that fails the
// transaction every iteration must not pin the worker forever.
func TestS10_6_WorkerMaxRowAttempts(t *testing.T) {
	t.Parallel()
	cfg := Config{}
	cfg.ApplyDefaults()
	if cfg.MaxRowAttempts <= 0 {
		t.Errorf("MaxRowAttempts default must be positive, got %d", cfg.MaxRowAttempts)
	}
	custom := Config{MaxRowAttempts: 3}
	custom.ApplyDefaults()
	if custom.MaxRowAttempts != 3 {
		t.Errorf("explicit MaxRowAttempts overridden by defaults: got %d", custom.MaxRowAttempts)
	}
}

// TestS10_3_InModifierReporter — S-10.3: ":in" modifier silent
// fail-closed; a reporter callback fires when the matcher hits an
// unsupported :in clause so wiring can bump a Prometheus counter.
func TestS10_3_InModifierReporter(t *testing.T) {
	t.Parallel()
	var hits []string
	SetUnsupportedModifierReporter(func(modifier, parameter string) {
		hits = append(hits, modifier+":"+parameter)
	})
	defer SetUnsupportedModifierReporter(nil)

	c := catalog.SearchClause{Parameter: "status", Modifier: "in", Value: "active|inactive"}
	if EvaluateClauseForTest(c, map[string]any{"status": "active"}) {
		t.Errorf(":in modifier should fail-closed (return false)")
	}
	if len(hits) != 1 || hits[0] != "in:status" {
		t.Errorf("reporter hits got %v want [in:status]", hits)
	}
}
