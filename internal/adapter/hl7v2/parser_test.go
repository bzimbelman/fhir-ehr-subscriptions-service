// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package hl7v2_test

import (
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/hl7v2"
)

const sampleADT = "MSH|^~\\&|EHR|FAC|RCV|FAC|20260618120000||ADT^A01|MSGID|P|2.5\r" +
	"EVN|A01|20260618120000\r" +
	"PID|1||PATID1234^5^M11^ADT1^MR^GOOD HEALTH HOSPITAL||EVERYWOMAN^EVE^E^^^^L||19620320|F||2106-3|153 FERNWOOD DR.^^STATESVILLE^OH^35292\r" +
	"PV1|1|I|2000^2012^01||||004777^GOODDOC^GOODSON^J|||SUR|||||||004777^GOODDOC^GOODSON^J|S|VisitNumber^^^Adt^VN|A\r"

const sampleORU = "MSH|^~\\&|LAB|FAC|RCV|FAC|20260618120000||ORU^R01|MSGID|P|2.5\r" +
	"PID|1||PATID5678||TESTPAT^FIRST||19800101|M\r" +
	"OBR|1|ORDER123||CBC^Complete Blood Count^L|||20260618110000\r" +
	"OBX|1|NM|GLU^Glucose^L||95|mg/dL|70-99|N|||F\r"

const sampleORM = "MSH|^~\\&|EHR|FAC|RCV|FAC|20260618120000||ORM^O01|MSGID|P|2.5\r" +
	"PID|1||ORDPID999||ORDER^PAT||19700101|M\r" +
	"ORC|NW|ORDNUM1|||||||20260618120000\r" +
	"OBR|1|ORDNUM1||LIPID^Lipid Panel^L|||20260618120000\r"

func TestParseExtractsMessageType(t *testing.T) {
	t.Parallel()
	msg, err := hl7v2.Parse([]byte(sampleADT))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := msg.MessageType(); got != "ADT^A01" {
		t.Errorf("MessageType = %q, want ADT^A01", got)
	}
	if got := msg.TriggerEvent(); got != "A01" {
		t.Errorf("TriggerEvent = %q, want A01", got)
	}
}

func TestParseExtractsPatientID(t *testing.T) {
	t.Parallel()
	msg, err := hl7v2.Parse([]byte(sampleADT))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	pid := msg.PatientID()
	if pid != "PATID1234" {
		t.Errorf("PatientID = %q, want PATID1234", pid)
	}
}

func TestParseHandlesLowercaseMSH(t *testing.T) {
	t.Parallel()
	// Meditech / Allscripts pre-2014 sometimes emit lowercase msh.
	lower := strings.Replace(sampleADT, "MSH", "msh", 1)
	msg, err := hl7v2.Parse([]byte(lower))
	if err != nil {
		t.Fatalf("Parse lowercase: %v", err)
	}
	if got := msg.MessageType(); got != "ADT^A01" {
		t.Errorf("MessageType = %q, want ADT^A01", got)
	}
}

func TestParseRejectsEmpty(t *testing.T) {
	t.Parallel()
	if _, err := hl7v2.Parse(nil); err == nil {
		t.Error("Parse(nil) succeeded; want error")
	}
	if _, err := hl7v2.Parse([]byte("")); err == nil {
		t.Error("Parse(empty) succeeded; want error")
	}
}

func TestParseRejectsMissingMSH(t *testing.T) {
	t.Parallel()
	if _, err := hl7v2.Parse([]byte("PID|1||PATID\r")); err == nil {
		t.Error("Parse without MSH succeeded; want error")
	}
}

func TestSegmentLookups(t *testing.T) {
	t.Parallel()
	msg, err := hl7v2.Parse([]byte(sampleADT))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	pid, ok := msg.Segment("PID")
	if !ok {
		t.Fatal("PID segment missing")
	}
	if got := pid.Field(5); got != "EVERYWOMAN^EVE^E^^^^L" {
		t.Errorf("PID-5 = %q", got)
	}
	if got := pid.Component(5, 1); got != "EVERYWOMAN" {
		t.Errorf("PID-5.1 = %q, want EVERYWOMAN", got)
	}
	if got := pid.Component(5, 2); got != "EVE" {
		t.Errorf("PID-5.2 = %q, want EVE", got)
	}
}

func TestSegmentsByName(t *testing.T) {
	t.Parallel()
	msg, err := hl7v2.Parse([]byte(sampleORU))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	obx := msg.Segments("OBX")
	if len(obx) != 1 {
		t.Errorf("OBX count = %d, want 1", len(obx))
	}
	if v := obx[0].Field(5); v != "95" {
		t.Errorf("OBX-5 = %q, want 95", v)
	}
}

func TestParseHandlesLFOnlyLineEndings(t *testing.T) {
	t.Parallel()
	// Some integration engines normalize \r to \n. Parser should accept both.
	lf := strings.ReplaceAll(sampleADT, "\r", "\n")
	msg, err := hl7v2.Parse([]byte(lf))
	if err != nil {
		t.Fatalf("Parse LF: %v", err)
	}
	if msg.PatientID() != "PATID1234" {
		t.Errorf("PatientID under LF endings = %q", msg.PatientID())
	}
}
