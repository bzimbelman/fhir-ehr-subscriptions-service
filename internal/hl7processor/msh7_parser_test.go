// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// HL7 v2 parser hardening — covers OP #194/#195/#196.
//
//	#194 honor MSH-2 escape characters in MSH-7 parser
//	#195 match MSH segment ID case-insensitively
//	#196 parse HL7 v2 timestamps with sub-second + offset support
//
// These tests directly exercise messageDateTime in processor.go (the
// MSH-7 walker / timestamp parser).

package hl7processor

import (
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

// mkParsed wraps raw HL7 bytes into a ParsedHL7Message — messageDateTime
// only inspects the Raw field.
func mkParsed(raw string) spi.ParsedHL7Message {
	return spi.ParsedHL7Message{Raw: []byte(raw)}
}

// --- OP #195: case-insensitive MSH segment ID ---

// TestMessageDateTime_LowercaseMSH — Allscripts pre-2014 / MEDITECH MAGIC
// emit lowercase "msh". The walker must accept it.
func TestMessageDateTime_LowercaseMSH(t *testing.T) {
	t.Parallel()
	raw := "msh|^~\\&|ALLSCRIPTS|TOUCHWORKS|REC|FAC|20260618120000||ADT^A01|M|P|2.5\r" +
		"PID|1||PATID1234\r"
	got, ok := messageDateTime(mkParsed(raw))
	if !ok {
		t.Fatalf("messageDateTime should accept lowercase msh; ok=false")
	}
	want := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("MSH-7 got %v want %v", got, want)
	}
}

// TestMessageDateTime_MixedCaseMSH — Mixed-case "Msh" must also be accepted.
func TestMessageDateTime_MixedCaseMSH(t *testing.T) {
	t.Parallel()
	raw := "Msh|^~\\&|VENDOR|FAC|REC|FAC|20260101000000||ADT^A04|M|P|2.5\r"
	got, ok := messageDateTime(mkParsed(raw))
	if !ok {
		t.Fatalf("messageDateTime should accept mixed-case Msh; ok=false")
	}
	want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("MSH-7 got %v want %v", got, want)
	}
}

// TestMessageDateTime_UppercaseStillWorks — guard against regression on
// the standard uppercase path.
func TestMessageDateTime_UppercaseStillWorks(t *testing.T) {
	t.Parallel()
	raw := "MSH|^~\\&|VENDOR|FAC|REC|FAC|20260618120000||ADT^A01|M|P|2.5\r"
	got, ok := messageDateTime(mkParsed(raw))
	if !ok {
		t.Fatalf("uppercase MSH must still parse; ok=false")
	}
	want := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("MSH-7 got %v want %v", got, want)
	}
}

// --- OP #194: MSH-2 escape character honored in field walker ---

// TestMessageDateTime_EscapedFieldSeparator — when MSH-2 declares the
// default escape '\' and a sender field contains a literal backslash-pipe
// (\|), the walker must skip the escaped pipe rather than count it as
// a field separator. Otherwise field counting drifts and MSH-7 is read
// from the wrong field (or returned empty).
//
// This vector is shaped after a real Cerner Z-segment quirk: a custom
// sending facility field containing an escape sequence ahead of MSH-7.
func TestMessageDateTime_EscapedFieldSeparator(t *testing.T) {
	t.Parallel()
	// Layout (1-indexed):
	//   MSH-1=|  MSH-2=^~\&  MSH-3=SENDER  MSH-4=FAC\|WITHPIPE  (escaped pipe inside)
	//   MSH-5=REC MSH-6=FAC MSH-7=20260101120000 MSH-8="" MSH-9=ADT^A01
	// Without escape handling the walker counts the \| as a real
	// separator and reads MSH-7 from the wrong field.
	raw := "MSH|^~\\&|SENDER|FAC\\|WITHPIPE|REC|FAC|20260101120000||ADT^A01|M|P|2.5\r"
	got, ok := messageDateTime(mkParsed(raw))
	if !ok {
		t.Fatalf("MSH-7 must parse with escaped pipe in MSH-4; ok=false")
	}
	want := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("MSH-7 got %v want %v (escape handling drifts the walker)", got, want)
	}
}

// TestMessageDateTime_CustomEscapeChar — MSH-2 is "^~#&" (third char
// declares '#' as escape). A field containing "#|" must not be counted
// as a separator.
func TestMessageDateTime_CustomEscapeChar(t *testing.T) {
	t.Parallel()
	// MSH-2 = ^~#& -- escape char is '#'.
	// MSH-4 contains "FAC#|WITHPIPE" — '#|' is an escaped pipe.
	raw := "MSH|^~#&|SENDER|FAC#|WITHPIPE|REC|FAC|20260202130405||ADT^A01|M|P|2.5\r"
	got, ok := messageDateTime(mkParsed(raw))
	if !ok {
		t.Fatalf("MSH-7 must parse with custom escape '#'; ok=false")
	}
	want := time.Date(2026, 2, 2, 13, 4, 5, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("MSH-7 got %v want %v (custom escape ignored)", got, want)
	}
}

// --- OP #196: HL7 v2 timestamps with sub-second + offset support ---

// TestMessageDateTime_FractionalSeconds — YYYYMMDDHHMMSS.SSS must parse,
// preserving sub-second precision (millisecond at minimum).
func TestMessageDateTime_FractionalSeconds(t *testing.T) {
	t.Parallel()
	raw := "MSH|^~\\&|VENDOR|FAC|REC|FAC|20260307081530.250||ADT^A01|M|P|2.5\r"
	got, ok := messageDateTime(mkParsed(raw))
	if !ok {
		t.Fatalf("MSH-7 with sub-second must parse; ok=false")
	}
	want := time.Date(2026, 3, 7, 8, 15, 30, 250*int(time.Millisecond), time.UTC)
	if !got.Equal(want) {
		t.Errorf("MSH-7 got %v want %v", got, want)
	}
}

// TestMessageDateTime_PositiveOffset — YYYYMMDDHHMMSS+ZZZZ must parse,
// with the wall-clock value preserved; result is normalized to UTC.
func TestMessageDateTime_PositiveOffset(t *testing.T) {
	t.Parallel()
	// 2026-03-07 12:00:00 +0500 == 2026-03-07 07:00:00 UTC
	raw := "MSH|^~\\&|VENDOR|FAC|REC|FAC|20260307120000+0500||ADT^A01|M|P|2.5\r"
	got, ok := messageDateTime(mkParsed(raw))
	if !ok {
		t.Fatalf("MSH-7 with +offset must parse; ok=false")
	}
	want := time.Date(2026, 3, 7, 7, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("MSH-7 got %v want %v", got, want)
	}
}

// TestMessageDateTime_NegativeOffset — YYYYMMDDHHMMSS-ZZZZ must parse,
// preserved in UTC.
func TestMessageDateTime_NegativeOffset(t *testing.T) {
	t.Parallel()
	// 2026-03-07 02:00:00 -0500 == 2026-03-07 07:00:00 UTC
	raw := "MSH|^~\\&|VENDOR|FAC|REC|FAC|20260307020000-0500||ADT^A01|M|P|2.5\r"
	got, ok := messageDateTime(mkParsed(raw))
	if !ok {
		t.Fatalf("MSH-7 with -offset must parse; ok=false")
	}
	want := time.Date(2026, 3, 7, 7, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("MSH-7 got %v want %v", got, want)
	}
}

// TestMessageDateTime_FractionalPlusOffset — YYYYMMDDHHMMSS.SSS-ZZZZ
// (the most common real-world Epic / Cerner shape).
func TestMessageDateTime_FractionalPlusOffset(t *testing.T) {
	t.Parallel()
	// 2026-03-07 02:00:00.125 -0500 == 2026-03-07 07:00:00.125 UTC
	raw := "MSH|^~\\&|EPIC|FAC|REC|FAC|20260307020000.125-0500||ADT^A01|M|P|2.5\r"
	got, ok := messageDateTime(mkParsed(raw))
	if !ok {
		t.Fatalf("MSH-7 with fractional+offset must parse; ok=false")
	}
	want := time.Date(2026, 3, 7, 7, 0, 0, 125*int(time.Millisecond), time.UTC)
	if !got.Equal(want) {
		t.Errorf("MSH-7 got %v want %v", got, want)
	}
}

// TestMessageDateTime_Unparseable_StillFalse — garbage must still return
// (zero, false) so the processor falls back to wall clock.
func TestMessageDateTime_Unparseable_StillFalse(t *testing.T) {
	t.Parallel()
	raw := "MSH|^~\\&|VENDOR|FAC|REC|FAC|not-a-timestamp||ADT^A01|M|P|2.5\r"
	if _, ok := messageDateTime(mkParsed(raw)); ok {
		t.Errorf("garbage MSH-7 must return ok=false")
	}
}
