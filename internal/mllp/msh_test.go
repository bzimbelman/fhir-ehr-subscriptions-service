// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

import (
	"testing"
)

func TestExtractMSH_Standard(t *testing.T) {
	// Standard MSH with default delimiters: |^~\&
	// Field positions: MSH-9 = ORU^R01 (msg type), MSH-10 = MSG-12345 (control id).
	body := []byte("MSH|^~\\&|SNDR|FAC|RCVR|RFAC|20240101010101||ORU^R01|MSG-12345|P|2.5\rPID|...\r")
	got, err := ExtractMSH(body)
	if err != nil {
		t.Fatalf("standard MSH must parse; err=%v", err)
	}
	if got.MessageType != "ORU" {
		t.Fatalf("MessageType = %q, want %q", got.MessageType, "ORU")
	}
	if got.MessageControlID != "MSG-12345" {
		t.Fatalf("MessageControlID = %q, want %q", got.MessageControlID, "MSG-12345")
	}
}

func TestExtractMSH_VendorZSegments(t *testing.T) {
	// Vendor Z-segments after MSH must not affect MSH-9 / MSH-10 extraction.
	body := []byte("MSH|^~\\&|EPIC|FAC|||20240101||ADT^A04|EVT-7777|P|2.5\rZID|EpicSpecific|x\rPID|...\r")
	got, err := ExtractMSH(body)
	if err != nil {
		t.Fatalf("vendor Z-segments after MSH must still parse; err=%v", err)
	}
	if got.MessageType != "ADT" {
		t.Fatalf("MessageType = %q, want %q", got.MessageType, "ADT")
	}
	if got.MessageControlID != "EVT-7777" {
		t.Fatalf("MessageControlID = %q, want %q", got.MessageControlID, "EVT-7777")
	}
}

func TestExtractMSH_Malformed_NotMSH(t *testing.T) {
	// First segment is not "MSH" — must error.
	body := []byte("PID|||...\rOBR|||...\r")
	if _, err := ExtractMSH(body); err == nil {
		t.Fatalf("expected error for first-segment != MSH")
	}
}

func TestExtractMSH_Malformed_TooShort(t *testing.T) {
	body := []byte("MS")
	if _, err := ExtractMSH(body); err == nil {
		t.Fatalf("expected error for too-short MSH")
	}
}

func TestExtractMSH_AlternateFieldSeparator(t *testing.T) {
	// Use '#' as field separator. Anything goes per HL7 v2 spec.
	body := []byte("MSH#^~\\&#SNDR#FAC###20240101##ORM^O01#CTRL-99#P#2.5\r")
	got, err := ExtractMSH(body)
	if err != nil {
		t.Fatalf("non-default field separator must parse; err=%v", err)
	}
	if got.MessageType != "ORM" {
		t.Fatalf("MessageType = %q, want %q", got.MessageType, "ORM")
	}
	if got.MessageControlID != "CTRL-99" {
		t.Fatalf("MessageControlID = %q, want %q", got.MessageControlID, "CTRL-99")
	}
}

func TestExtractMSH_TripleComponentMSH9(t *testing.T) {
	// MSH-9 with structure subcomponent: ORU^R01^ORU_R01 — root type is "ORU".
	body := []byte("MSH|^~\\&|SNDR|FAC|||20240101||ORU^R01^ORU_R01|MSG-T|P|2.5\r")
	got, err := ExtractMSH(body)
	if err != nil {
		t.Fatalf("triple-component MSH-9 must parse; err=%v", err)
	}
	if got.MessageType != "ORU" {
		t.Fatalf("MessageType = %q, want %q", got.MessageType, "ORU")
	}
	if got.MessageControlID != "MSG-T" {
		t.Fatalf("MessageControlID = %q, want %q", got.MessageControlID, "MSG-T")
	}
}

func TestExtractMSH_MSH10Missing(t *testing.T) {
	// MSH-9 present, MSH-10 empty — extraction succeeds with empty control ID.
	body := []byte("MSH|^~\\&|SNDR|FAC|||20240101||ADT^A01||P|2.5\r")
	got, err := ExtractMSH(body)
	if err != nil {
		t.Fatalf("missing MSH-10 must still parse; err=%v", err)
	}
	if got.MessageType != "ADT" {
		t.Fatalf("MessageType = %q, want %q", got.MessageType, "ADT")
	}
	if got.MessageControlID != "" {
		t.Fatalf("MessageControlID should be empty; got %q", got.MessageControlID)
	}
}

func TestExtractMSH_NoBody(t *testing.T) {
	if _, err := ExtractMSH(nil); err == nil {
		t.Fatalf("expected error on nil body")
	}
	if _, err := ExtractMSH([]byte{}); err == nil {
		t.Fatalf("expected error on empty body")
	}
}

func TestExtractMSH_NoTrailingCR(t *testing.T) {
	// Body has no segment terminator at all — first segment is the whole body.
	body := []byte("MSH|^~\\&|SNDR|FAC|||20240101||SIU^S12|MSG-S|P|2.5")
	got, err := ExtractMSH(body)
	if err != nil {
		t.Fatalf("no-trailing-CR must still parse; err=%v", err)
	}
	if got.MessageType != "SIU" {
		t.Fatalf("MessageType = %q, want %q", got.MessageType, "SIU")
	}
	if got.MessageControlID != "MSG-S" {
		t.Fatalf("MessageControlID = %q, want %q", got.MessageControlID, "MSG-S")
	}
}
