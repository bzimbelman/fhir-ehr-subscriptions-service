// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package hl7

import (
	"strings"
	"testing"
	"time"
)

// HL7 v2 builders are the demo CLIs' payload generators. They emit the same
// byte-perfect ER7 wire format as e2e/mockehr so the demo can round-trip
// against the real fhir-subs MLLP listener (per OP #158, demo CLIs must not
// import e2e scaffolding).
//
// These tests pin the contract: separators, MSH-9/10, segment terminators,
// and that defaults are applied.

func TestBuildADT_DefaultsAndExplicitFields(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	msg := BuildADT(ADTOptions{
		TriggerEvent:  "A01",
		MessageID:     "ADT0001",
		PatientID:     "MRN1",
		PatientFamily: "Smith",
		PatientGiven:  "Bob",
		Now:           fixed,
	})
	if !strings.HasPrefix(msg, "MSH|^~\\&|MOCKEHR|E2E|FHIRSUBS|TEST|20260619120000|") {
		t.Fatalf("MSH header wrong, got: %s", msg)
	}
	if !strings.Contains(msg, "|ADT^A01|") {
		t.Fatalf("expected MSH-9=ADT^A01, got: %s", msg)
	}
	if !strings.Contains(msg, "|ADT0001|") {
		t.Fatalf("expected MSH-10=ADT0001, got: %s", msg)
	}
	if !strings.Contains(msg, "|T|2.5.1\r") {
		t.Fatalf("expected processing-id+version-id segment tail, got: %s", msg)
	}
	if !strings.Contains(msg, "EVN|A01|20260619120000\r") {
		t.Fatalf("expected EVN segment with trigger+ts, got: %s", msg)
	}
	if !strings.Contains(msg, "PID|1||MRN1^^^E2E^MR||Smith^Bob\r") {
		t.Fatalf("expected PID with explicit names, got: %s", msg)
	}
	if !strings.HasSuffix(msg, "\r") {
		t.Fatalf("HL7 message must end with \\r, got tail: %q", msg[len(msg)-3:])
	}
}

func TestBuildADT_DefaultTriggerAndNames(t *testing.T) {
	t.Parallel()
	msg := BuildADT(ADTOptions{
		MessageID: "ADT-NODEFAULTS",
		PatientID: "P1",
	})
	// Empty TriggerEvent defaults to A01.
	if !strings.Contains(msg, "|ADT^A01|") {
		t.Fatalf("expected default trigger ADT^A01, got: %s", msg)
	}
	// Empty patient name defaults to Doe^Jane.
	if !strings.Contains(msg, "Doe^Jane") {
		t.Fatalf("expected default patient name Doe^Jane, got: %s", msg)
	}
	if !strings.Contains(msg, "EVN|A01|") {
		t.Fatalf("expected EVN with default trigger, got: %s", msg)
	}
}

func TestBuildADT_NowDefaultsToCurrentUTC(t *testing.T) {
	t.Parallel()
	before := time.Now().UTC()
	msg := BuildADT(ADTOptions{TriggerEvent: "A01", MessageID: "X", PatientID: "P"})
	after := time.Now().UTC()

	// Extract the MSH-7 timestamp (8th field after MSH+separators) — easier
	// to scan: we know it precedes the empty MSH-8 then ADT^A01.
	idx := strings.Index(msg, "||ADT^A01|")
	if idx < 0 {
		t.Fatalf("could not locate MSH-8 boundary in: %s", msg)
	}
	// Walk back 14 chars (YYYYMMDDHHMMSS) to recover MSH-7.
	if idx < 14 {
		t.Fatalf("MSH header too short: %s", msg)
	}
	tsField := msg[idx-14 : idx]
	parsed, err := time.Parse("20060102150405", tsField)
	if err != nil {
		t.Fatalf("MSH-7 not in YYYYMMDDHHMMSS format: %q (%v)", tsField, err)
	}
	if parsed.Before(before.Truncate(time.Second).Add(-time.Second)) ||
		parsed.After(after.Add(time.Second)) {
		t.Fatalf("MSH-7 default %v not in [%v,%v]", parsed, before, after)
	}
}

func TestBuildORU_AllFieldsWired(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	msg := BuildORU(ORUOptions{
		MessageID: "ORU0001",
		PatientID: "MRN9",
		Result: ORUResult{
			ObservationID: "GLU^Glucose^L",
			Value:         "92",
			Unit:          "mg/dL",
			RefRange:      "70-99",
			AbnormalFlag:  "N",
		},
		Now: fixed,
	})
	if !strings.Contains(msg, "|ORU^R01|ORU0001|") {
		t.Fatalf("expected MSH-9=ORU^R01 with control id, got: %s", msg)
	}
	if !strings.Contains(msg, "PID|1||MRN9^^^E2E^MR||Doe^Jane\r") {
		t.Fatalf("expected PID with default name, got: %s", msg)
	}
	if !strings.Contains(msg, "OBR|1|||GLU^Glucose^L|||20260102030405\r") {
		t.Fatalf("expected OBR with observation id and ts, got: %s", msg)
	}
	if !strings.Contains(msg, "OBX|1|NM|GLU^Glucose^L||92|mg/dL|70-99|N|||F\r") {
		t.Fatalf("expected fully-populated OBX, got: %s", msg)
	}
}

func TestBuildORU_NowDefaultsToCurrentUTC(t *testing.T) {
	t.Parallel()
	before := time.Now().UTC().Truncate(time.Second).Add(-time.Second)
	msg := BuildORU(ORUOptions{
		MessageID: "ORU-AUTO",
		PatientID: "P",
		Result:    ORUResult{ObservationID: "X^Y^Z", Value: "1"},
	})
	after := time.Now().UTC().Add(time.Second)

	// Pull MSH-7 the same way as the ADT test.
	idx := strings.Index(msg, "||ORU^R01|")
	if idx < 14 {
		t.Fatalf("MSH header too short: %s", msg)
	}
	tsField := msg[idx-14 : idx]
	parsed, err := time.Parse("20060102150405", tsField)
	if err != nil {
		t.Fatalf("MSH-7 not in YYYYMMDDHHMMSS format: %q (%v)", tsField, err)
	}
	if parsed.Before(before) || parsed.After(after) {
		t.Fatalf("MSH-7 default %v not in [%v,%v]", parsed, before, after)
	}
}

func TestBuilders_NoMLLPFramingBytes(t *testing.T) {
	t.Parallel()
	// MLLP framing is the caller's responsibility (see mllp pkg). Builders
	// must not embed 0x0B or 0x1C.
	cases := []string{
		BuildADT(ADTOptions{TriggerEvent: "A01", MessageID: "x", PatientID: "p"}),
		BuildORU(ORUOptions{MessageID: "x", PatientID: "p", Result: ORUResult{ObservationID: "a^b^c", Value: "1"}}),
	}
	for i, m := range cases {
		if strings.ContainsAny(m, "\x0b\x1c") {
			t.Fatalf("case %d: builder embedded MLLP framing bytes", i)
		}
		if !strings.HasSuffix(m, "\r") {
			t.Fatalf("case %d: builder must end with \\r, got tail: %q", i, m[len(m)-3:])
		}
	}
}

func TestBuildADT_TriggerEventA08PreservesField(t *testing.T) {
	t.Parallel()
	// Non-default trigger threads through both MSH-9 and EVN-1.
	msg := BuildADT(ADTOptions{
		TriggerEvent: "A08",
		MessageID:    "X",
		PatientID:    "P",
	})
	if !strings.Contains(msg, "|ADT^A08|") {
		t.Fatalf("expected MSH-9=ADT^A08, got: %s", msg)
	}
	if !strings.Contains(msg, "EVN|A08|") {
		t.Fatalf("expected EVN|A08|, got: %s", msg)
	}
}
