// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package mockehr

import (
	"strings"
	"testing"
)

// HL7 message builders are the EHR-side payload generators used by the
// scenario control plane. They emit byte-perfect HL7 v2.x ER7 strings that
// the MLLP framer in fhir-subs is expected to parse. The MLLP listener only
// reads MSH-9 (message type) and MSH-10 (message control id); the rest of
// the body is opaque to the listener but must be well-formed because
// downstream adapters (the HL7 Message Processor) parse it for real.
//
// These tests pin the contract:
//   * Field separator MSH-1 is `|`, encoding chars MSH-2 are `^~\&`.
//   * MSH-9 carries the trigger event (e.g., `ADT^A01`, `ORM^O01`).
//   * MSH-10 carries the per-message control id provided by the caller.
//   * Segments terminate with `\r` (carriage return) — the canonical HL7
//     segment terminator. The framer tolerates `\r\n`, but builders emit
//     `\r` only because that is what every conformance test in HL7 v2 uses.

func TestBuildADT_A01_HasMSH9AndMSH10(t *testing.T) {
	t.Parallel()
	msg := BuildADT(ADTOptions{
		TriggerEvent:  "A01",
		MessageID:     "ADT0001",
		PatientID:     "MRN12345",
		PatientFamily: "Doe",
		PatientGiven:  "Jane",
	})
	want := "ADT^A01"
	if !strings.Contains(msg, "|"+want+"|") {
		t.Fatalf("expected MSH-9 = %q in message, got: %s", want, msg)
	}
	if !strings.Contains(msg, "|ADT0001|") {
		t.Fatalf("expected MSH-10 = ADT0001 in message, got: %s", msg)
	}
	if !strings.Contains(msg, "PID|") {
		t.Fatalf("expected PID segment in ADT message, got: %s", msg)
	}
	if !strings.HasPrefix(msg, "MSH|^~\\&|") {
		t.Fatalf("expected MSH header prefix `MSH|^~\\&|`, got: %.30s", msg)
	}
	if strings.Contains(msg, "\n") && !strings.Contains(msg, "\r") {
		t.Fatalf("HL7 messages must use \\r as segment terminator, got LF only")
	}
}

func TestBuildORM_O01_NewOrder_HasORC1AndOBR(t *testing.T) {
	t.Parallel()
	msg := BuildORM(ORMOptions{
		ControlCode:    ORCControlNew,
		MessageID:      "ORM0001",
		PlacerOrderID:  "PL-1001",
		FillerOrderID:  "",
		PatientID:      "MRN12345",
		UniversalSvcID: "CBC^Complete Blood Count^L",
	})
	if !strings.Contains(msg, "|ORM^O01|") {
		t.Fatalf("expected MSH-9 = ORM^O01, got: %s", msg)
	}
	// ORC-1 = NW for new order
	if !strings.Contains(msg, "ORC|NW|") {
		t.Fatalf("expected ORC|NW| (new order) segment, got: %s", msg)
	}
	if !strings.Contains(msg, "OBR|") {
		t.Fatalf("expected OBR segment, got: %s", msg)
	}
	if !strings.Contains(msg, "PL-1001") {
		t.Fatalf("expected placer order id PL-1001 in message, got: %s", msg)
	}
}

// Cancel + replacement linkage: ORC-2 is the placer order id, ORC-3 is the
// filler. The cancel message and the replacement message share a common
// placer/filler tuple — that is what the HL7 Message Processor uses to
// pair them inside the correlation_hold_window.
func TestBuildORM_CancelAndReplacement_ShareORC2ORC3(t *testing.T) {
	t.Parallel()
	cancel := BuildORM(ORMOptions{
		ControlCode:   ORCControlCancel,
		MessageID:     "ORM-CA-001",
		PlacerOrderID: "PL-2001",
		FillerOrderID: "FL-2001",
		PatientID:     "MRN12345",
	})
	replace := BuildORM(ORMOptions{
		ControlCode:   ORCControlNew,
		MessageID:     "ORM-RE-001",
		PlacerOrderID: "PL-2001",
		FillerOrderID: "FL-2001",
		PatientID:     "MRN12345",
	})

	if !strings.Contains(cancel, "ORC|CA|") {
		t.Fatalf("expected ORC|CA| in cancel, got: %s", cancel)
	}
	if !strings.Contains(replace, "ORC|NW|") {
		t.Fatalf("expected ORC|NW| in replacement, got: %s", replace)
	}
	for _, m := range []string{cancel, replace} {
		if !strings.Contains(m, "|PL-2001|") {
			t.Fatalf("expected placer order id PL-2001 in: %s", m)
		}
		if !strings.Contains(m, "|FL-2001|") {
			t.Fatalf("expected filler order id FL-2001 in: %s", m)
		}
	}
}

func TestBuildORU_R01_HasOBX(t *testing.T) {
	t.Parallel()
	msg := BuildORU(ORUOptions{
		MessageID: "ORU0001",
		PatientID: "MRN12345",
		Result: ORUResult{
			ObservationID: "GLU^Glucose^L",
			Value:         "92",
			Unit:          "mg/dL",
			RefRange:      "70-99",
			AbnormalFlag:  "N",
		},
	})
	if !strings.Contains(msg, "|ORU^R01|") {
		t.Fatalf("expected MSH-9 = ORU^R01, got: %s", msg)
	}
	if !strings.Contains(msg, "OBX|") {
		t.Fatalf("expected OBX segment in ORU, got: %s", msg)
	}
	if !strings.Contains(msg, "92") || !strings.Contains(msg, "mg/dL") {
		t.Fatalf("expected value 92 mg/dL in OBX, got: %s", msg)
	}
}

func TestBuildSIU_S12_HasSCH(t *testing.T) {
	t.Parallel()
	msg := BuildSIU(SIUOptions{
		TriggerEvent: "S12",
		MessageID:    "SIU0001",
		PatientID:    "MRN12345",
		ApptID:       "APPT-9001",
	})
	if !strings.Contains(msg, "|SIU^S12|") {
		t.Fatalf("expected MSH-9 = SIU^S12, got: %s", msg)
	}
	if !strings.Contains(msg, "SCH|") {
		t.Fatalf("expected SCH segment in SIU, got: %s", msg)
	}
}

func TestBuildMDM_T02_HasTXA(t *testing.T) {
	t.Parallel()
	msg := BuildMDM(MDMOptions{
		TriggerEvent: "T02",
		MessageID:    "MDM0001",
		PatientID:    "MRN12345",
		DocType:      "CN",
		DocID:        "DOC-1",
	})
	if !strings.Contains(msg, "|MDM^T02|") {
		t.Fatalf("expected MSH-9 = MDM^T02, got: %s", msg)
	}
	if !strings.Contains(msg, "TXA|") {
		t.Fatalf("expected TXA segment in MDM, got: %s", msg)
	}
}

// Every builder must produce HL7 with \r segment terminators, no embedded
// \x0B or \x1C\x0D MLLP framing bytes — the MLLP wrapping is the caller's
// job (see mllp_test.go).
func TestBuildersUseCarriageReturnSegmentTerminator(t *testing.T) {
	t.Parallel()
	cases := []string{
		BuildADT(ADTOptions{TriggerEvent: "A01", MessageID: "x", PatientID: "p"}),
		BuildORM(ORMOptions{ControlCode: ORCControlNew, MessageID: "x", PlacerOrderID: "p"}),
		BuildORU(ORUOptions{MessageID: "x", PatientID: "p", Result: ORUResult{ObservationID: "a^b^c", Value: "1"}}),
		BuildSIU(SIUOptions{TriggerEvent: "S12", MessageID: "x", PatientID: "p", ApptID: "a"}),
		BuildMDM(MDMOptions{TriggerEvent: "T02", MessageID: "x", PatientID: "p"}),
	}
	for i, m := range cases {
		if !strings.HasSuffix(m, "\r") {
			t.Errorf("case %d: builder must end with \\r, got tail %q", i, lastN(m, 4))
		}
		if strings.ContainsAny(m, "\x0b\x1c") {
			t.Errorf("case %d: builder must not include MLLP framing bytes 0x0B/0x1C", i)
		}
	}
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
