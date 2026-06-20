// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package hl7v2_test

import (
	"encoding/json"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/hl7v2"
)

func TestMapADT_A01_BuildsPatientAndEncounterBundle(t *testing.T) {
	t.Parallel()
	msg, err := hl7v2.Parse([]byte(sampleADT))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	bundle, err := hl7v2.MapToFHIR(msg)
	if err != nil {
		t.Fatalf("MapToFHIR: %v", err)
	}
	var b map[string]any
	if err := json.Unmarshal(bundle.Body, &b); err != nil {
		t.Fatalf("unmarshal bundle: %v\n%s", err, string(bundle.Body))
	}
	if b["resourceType"] != "Bundle" {
		t.Errorf("resourceType = %v", b["resourceType"])
	}
	entries, _ := b["entry"].([]any)
	if len(entries) < 2 {
		t.Fatalf("entries = %d, want at least 2 (Patient + Encounter)", len(entries))
	}
	got := make(map[string]bool)
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		res, _ := entry["resource"].(map[string]any)
		rt, _ := res["resourceType"].(string)
		got[rt] = true
	}
	if !got["Patient"] {
		t.Error("Bundle missing Patient")
	}
	if !got["Encounter"] {
		t.Error("Bundle missing Encounter")
	}
}

func TestMapADT_A01_PatientCarriesIdentifierName(t *testing.T) {
	t.Parallel()
	msg, _ := hl7v2.Parse([]byte(sampleADT))
	bundle, err := hl7v2.MapToFHIR(msg)
	if err != nil {
		t.Fatalf("MapToFHIR: %v", err)
	}
	// The PATID1234 value from PID-3 must appear somewhere in the Patient
	// resource — this is the strict acceptance criterion that proves
	// MapToFHIR is no longer a hardcoded passthrough.
	if !contains(bundle.Body, "PATID1234") {
		t.Fatalf("bundle body does not contain PATID1234:\n%s", string(bundle.Body))
	}
	if !contains(bundle.Body, "EVERYWOMAN") {
		t.Errorf("bundle body missing patient family name:\n%s", string(bundle.Body))
	}
}

func TestMapORU_R01_BuildsObservationBundle(t *testing.T) {
	t.Parallel()
	msg, _ := hl7v2.Parse([]byte(sampleORU))
	bundle, err := hl7v2.MapToFHIR(msg)
	if err != nil {
		t.Fatalf("MapToFHIR: %v", err)
	}
	var b map[string]any
	if err := json.Unmarshal(bundle.Body, &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	entries, _ := b["entry"].([]any)
	got := make(map[string]bool)
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		res, _ := entry["resource"].(map[string]any)
		rt, _ := res["resourceType"].(string)
		got[rt] = true
	}
	if !got["Patient"] {
		t.Error("ORU bundle missing Patient")
	}
	if !got["Observation"] {
		t.Error("ORU bundle missing Observation")
	}
	if !got["DiagnosticReport"] {
		t.Error("ORU bundle missing DiagnosticReport")
	}
	if !contains(bundle.Body, "PATID5678") {
		t.Errorf("ORU bundle missing patient id:\n%s", string(bundle.Body))
	}
	if !contains(bundle.Body, "Glucose") {
		t.Errorf("ORU bundle missing OBX text:\n%s", string(bundle.Body))
	}
}

func TestMapORM_O01_BuildsServiceRequestBundle(t *testing.T) {
	t.Parallel()
	msg, _ := hl7v2.Parse([]byte(sampleORM))
	bundle, err := hl7v2.MapToFHIR(msg)
	if err != nil {
		t.Fatalf("MapToFHIR: %v", err)
	}
	var b map[string]any
	if err := json.Unmarshal(bundle.Body, &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	entries, _ := b["entry"].([]any)
	got := make(map[string]bool)
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		res, _ := entry["resource"].(map[string]any)
		rt, _ := res["resourceType"].(string)
		got[rt] = true
	}
	if !got["Patient"] {
		t.Error("ORM bundle missing Patient")
	}
	if !got["ServiceRequest"] {
		t.Error("ORM bundle missing ServiceRequest")
	}
	if !contains(bundle.Body, "ORDPID999") {
		t.Errorf("ORM bundle missing patient id:\n%s", string(bundle.Body))
	}
	if !contains(bundle.Body, "ORDNUM1") {
		t.Errorf("ORM bundle missing order id:\n%s", string(bundle.Body))
	}
}

func TestMapUnknownMessageType_ReturnsBundleWithPatient(t *testing.T) {
	t.Parallel()
	// Adapter should not fail noisily on an unknown message; build a minimal
	// Patient-only Bundle so the framework's resource_changes pipeline still
	// has something to write.
	raw := "MSH|^~\\&|EHR|FAC|RCV|FAC|20260618120000||SIU^S12|MSGID|P|2.5\r" +
		"PID|1||UNK999||UNKNOWN^PAT||19900101|F\r"
	msg, err := hl7v2.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	bundle, err := hl7v2.MapToFHIR(msg)
	if err != nil {
		t.Fatalf("MapToFHIR: %v", err)
	}
	if bundle.ResourceType != "Bundle" {
		t.Errorf("ResourceType = %q", bundle.ResourceType)
	}
	if !contains(bundle.Body, "UNK999") {
		t.Errorf("body missing patient id:\n%s", string(bundle.Body))
	}
}

func contains(b []byte, s string) bool {
	return indexOf(b, s) >= 0
}

func indexOf(b []byte, s string) int {
	if len(s) == 0 {
		return 0
	}
loop:
	for i := 0; i+len(s) <= len(b); i++ {
		for j := 0; j < len(s); j++ {
			if b[i+j] != s[j] {
				continue loop
			}
		}
		return i
	}
	return -1
}
