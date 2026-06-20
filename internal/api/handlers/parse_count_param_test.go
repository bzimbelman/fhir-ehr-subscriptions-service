// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"testing"
)

// OP #189 RED — parseCountParam currently uses fmt.Sscanf which
// silently accepts trailing junk: "10malicious" parses as 10.  That
// pattern bypasses an attacker's input being flagged at the parameter
// boundary. The AC tightens this to strconv.Atoi semantics with three
// extra rules:
//   - reject any input that does NOT consist solely of ASCII digits
//   - reject leading sign characters (`+10`, `-5`)
//   - reject values that would clamp to MaxPageSize (i.e. reject
//     instead of silently capping; operators should see a 400)
//
// Today the function lives in internal/api/handlers/subscription_handlers.go
// (lowercase parseCountParam) and is package-private, so this test runs
// in the same `handlers` package (no `_test` suffix).

func TestParseCountParam_RejectsTrailingJunk(t *testing.T) {
	t.Parallel()
	// "10malicious" must be rejected. Today fmt.Sscanf("%d") accepts
	// the leading 10 and silently drops "malicious".
	if got, ok := parseCountParam("10malicious", 20, 100); ok {
		t.Errorf(`parseCountParam("10malicious") = (%d, true), want (_, false)`, got)
	}
}

func TestParseCountParam_RejectsLeadingPlusSign(t *testing.T) {
	t.Parallel()
	if got, ok := parseCountParam("+10", 20, 100); ok {
		t.Errorf(`parseCountParam("+10") = (%d, true), want (_, false)`, got)
	}
}

func TestParseCountParam_RejectsTrailingWhitespace(t *testing.T) {
	t.Parallel()
	// strconv.Atoi rejects whitespace. The current Sscanf path is
	// permissive about it. AC: trailing whitespace must be rejected.
	if got, ok := parseCountParam("10  ", 20, 100); ok {
		t.Errorf(`parseCountParam("10  ") = (%d, true), want (_, false)`, got)
	}
}

func TestParseCountParam_RejectsHexLiteral(t *testing.T) {
	t.Parallel()
	if got, ok := parseCountParam("0x10", 20, 100); ok {
		t.Errorf(`parseCountParam("0x10") = (%d, true), want (_, false)`, got)
	}
}

func TestParseCountParam_AcceptsCanonicalDigits(t *testing.T) {
	t.Parallel()
	got, ok := parseCountParam("10", 20, 100)
	if !ok {
		t.Fatalf(`parseCountParam("10") ok=false, want true`)
	}
	if got != 10 {
		t.Errorf(`parseCountParam("10") = %d, want 10`, got)
	}
}

func TestParseCountParam_EmptyReturnsDefault(t *testing.T) {
	t.Parallel()
	got, ok := parseCountParam("", 20, 100)
	if !ok || got != 20 {
		t.Errorf(`parseCountParam("") = (%d, %v), want (20, true)`, got, ok)
	}
}

func TestParseCountParam_RejectsNegative(t *testing.T) {
	t.Parallel()
	if got, ok := parseCountParam("-5", 20, 100); ok {
		t.Errorf(`parseCountParam("-5") = (%d, true), want (_, false)`, got)
	}
}

// TestParseCountParam_RejectsValueAboveMaxPageSize — AC inverts the
// current "clamp silently" behavior to "reject". An operator who set
// max=100 wants `_count=999` to come back as 400, not silently
// answered with 100 rows.
func TestParseCountParam_RejectsValueAboveMaxPageSize(t *testing.T) {
	t.Parallel()
	if got, ok := parseCountParam("999", 20, 100); ok {
		t.Errorf(`parseCountParam("999", max=100) = (%d, true), want (_, false) — AC requires reject, not clamp`, got)
	}
}
