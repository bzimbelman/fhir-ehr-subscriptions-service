// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package demoadapter_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	demoadapter "github.com/bzimbelman/fhir-ehr-subscriptions-service/adapters/demo"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/registry"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

// The demo adapter is a tiny illustrative HL7-to-FHIR translator: it shows
// what a vendor adapter might do beyond the default passthrough. The unit
// test asserts the translation is good enough that a downstream matcher can
// see patient=<MRN>, the OBX-3 LOINC code, the OBX-5 value, and the MSH-7
// effectiveDateTime on the produced Observation.

func TestDemoManifestShape(t *testing.T) {
	t.Parallel()
	a := demoadapter.New()
	m := a.Manifest()
	if m.ID != "demo" {
		t.Errorf("Manifest.ID = %q, want demo", m.ID)
	}
	if !m.Capabilities.HL7Processor {
		t.Error("demo adapter should declare HL7Processor capability")
	}
	if m.SpiVersion != spi.HostSPIVersion {
		t.Errorf("Manifest.SpiVersion = %v, want %v", m.SpiVersion, spi.HostSPIVersion)
	}
	if err := m.Validate(); err != nil {
		t.Errorf("Manifest.Validate() = %v, want nil", err)
	}
}

func TestDemoManifestIsRegistrable(t *testing.T) {
	t.Parallel()
	r := demoadapter.NewRegistered()
	a, err := r.Load(context.Background(), registry.LoadConfig{
		AdapterID:  "demo",
		HostSpiVer: spi.HostSPIVersion,
	})
	if err != nil {
		t.Fatalf("registry.Load(demo) = %v, want nil", err)
	}
	if a == nil || a.Manifest().ID != "demo" {
		t.Errorf("loaded adapter id = %q, want demo", a.Manifest().ID)
	}
}

// buildORU constructs a minimal ER7-encoded ORU^R01 message inline so the
// unit test does not depend on the e2e mockehr builders. Segment terminator
// is \r per HL7 v2; field separator is |.
func buildORU(mrn, msh7 string) []byte {
	msh := "MSH|^~\\&|MOCKEHR|E2E|FHIRSUBS|TEST|" + msh7 + "||ORU^R01|MSG0001|T|2.5.1"
	pid := "PID|1||" + mrn + "^^^E2E^MR||Doe^Jane"
	obr := "OBR|1|||1234-5^Glucose^LN|||" + msh7
	obx := "OBX|1|NM|1234-5^Glucose^LN||5.4|mmol/L|3.9-5.6|N|||F"
	return []byte(strings.Join([]string{msh, pid, obr, obx}, "\r") + "\r")
}

func TestDemoLexORU(t *testing.T) {
	t.Parallel()
	p := demoadapter.New().BuildHl7Processor(spi.AdapterContext{Now: time.Now})
	if p == nil {
		t.Fatal("BuildHl7Processor returned nil")
	}
	raw := buildORU("ABC123", "20260418101500")
	parsed, err := p.Lex(raw)
	if err != nil {
		t.Fatalf("Lex = %v, want nil", err)
	}
	if string(parsed.Raw) != string(raw) {
		t.Errorf("Lex did not preserve raw bytes")
	}
	if parsed.Segments == nil {
		t.Error("Lex.Segments is nil; expected populated segment tree for ORU^R01")
	}
}

func TestDemoClassifyORU(t *testing.T) {
	t.Parallel()
	p := demoadapter.New().BuildHl7Processor(spi.AdapterContext{Now: time.Now})
	parsed, err := p.Lex(buildORU("ABC123", "20260418101500"))
	if err != nil {
		t.Fatalf("Lex = %v, want nil", err)
	}
	cls, err := p.Classify(parsed)
	if err != nil {
		t.Fatalf("Classify = %v, want nil", err)
	}
	if cls.Kind != spi.ChangeCreate {
		t.Errorf("Classify.Kind = %v, want create (ORU^R01 carries a new result)", cls.Kind)
	}
}

// TestDemoMapToFHIR_ORUtoObservation is the load-bearing unit test: the
// Observation produced from an ORU^R01 must carry the OBX-3 LOINC code,
// the OBX-5 numeric value as Observation.valueQuantity, the MSH-7 timestamp
// as effectiveDateTime, and the PID-3 MRN as the subject reference.
func TestDemoMapToFHIR_ORUtoObservation(t *testing.T) {
	t.Parallel()
	p := demoadapter.New().BuildHl7Processor(spi.AdapterContext{Now: time.Now})

	raw := buildORU("ABC123", "20260418101500")
	parsed, err := p.Lex(raw)
	if err != nil {
		t.Fatalf("Lex = %v, want nil", err)
	}
	cls, err := p.Classify(parsed)
	if err != nil {
		t.Fatalf("Classify = %v, want nil", err)
	}

	resource, err := p.MapToFHIR(parsed, cls)
	if err != nil {
		t.Fatalf("MapToFHIR = %v, want nil", err)
	}
	if resource.ResourceType != "Observation" {
		t.Errorf("ResourceType = %q, want Observation", resource.ResourceType)
	}
	if resource.ID == "" {
		t.Error("FhirResource.ID empty; demo adapter should mint a stable id from MSH-10")
	}

	var obs map[string]any
	if err := json.Unmarshal(resource.Body, &obs); err != nil {
		t.Fatalf("Body is not JSON: %v\nbody=%s", err, resource.Body)
	}

	if obs["resourceType"] != "Observation" {
		t.Errorf("body.resourceType = %v, want Observation", obs["resourceType"])
	}
	if status, _ := obs["status"].(string); status != "final" {
		t.Errorf("body.status = %q, want final (OBX-11 = F)", status)
	}

	// subject -> "Patient/ABC123"
	subj, _ := obs["subject"].(map[string]any)
	if got := subj["reference"]; got != "Patient/ABC123" {
		t.Errorf("subject.reference = %v, want Patient/ABC123", got)
	}

	// effectiveDateTime -> 2026-04-18T10:15:00Z (from MSH-7)
	if got := obs["effectiveDateTime"]; got != "2026-04-18T10:15:00Z" {
		t.Errorf("effectiveDateTime = %v, want 2026-04-18T10:15:00Z", got)
	}

	// code.coding -> LOINC 1234-5
	code, _ := obs["code"].(map[string]any)
	codings, _ := code["coding"].([]any)
	if len(codings) == 0 {
		t.Fatalf("code.coding empty; want LOINC 1234-5")
	}
	c0, _ := codings[0].(map[string]any)
	if c0["system"] != "http://loinc.org" {
		t.Errorf("code.coding[0].system = %v, want http://loinc.org", c0["system"])
	}
	if c0["code"] != "1234-5" {
		t.Errorf("code.coding[0].code = %v, want 1234-5", c0["code"])
	}
	if c0["display"] != "Glucose" {
		t.Errorf("code.coding[0].display = %v, want Glucose", c0["display"])
	}

	// valueQuantity -> 5.4 mmol/L
	vq, _ := obs["valueQuantity"].(map[string]any)
	if got, _ := vq["value"].(float64); got != 5.4 {
		t.Errorf("valueQuantity.value = %v, want 5.4", got)
	}
	if got := vq["unit"]; got != "mmol/L" {
		t.Errorf("valueQuantity.unit = %v, want mmol/L", got)
	}
}

// Non-numeric OBX-2 (e.g., ST = string) must fall back to valueString so the
// Observation still validates as a recognizable FHIR shape.
func TestDemoMapToFHIR_ORUStringValue(t *testing.T) {
	t.Parallel()
	p := demoadapter.New().BuildHl7Processor(spi.AdapterContext{Now: time.Now})

	msh := "MSH|^~\\&|MOCKEHR|E2E|FHIRSUBS|TEST|20260418101500||ORU^R01|MSG0002|T|2.5.1"
	pid := "PID|1||XYZ789^^^E2E^MR||Doe^Jane"
	obr := "OBR|1|||TXT^Comment^L|||20260418101500"
	// OBX-2 = ST (string); OBX-5 = "see note"; OBX-11 = C (corrected)
	obx := "OBX|1|ST|TXT^Comment^L||see note||||||C"
	raw := []byte(strings.Join([]string{msh, pid, obr, obx}, "\r") + "\r")

	parsed, _ := p.Lex(raw)
	cls, _ := p.Classify(parsed)
	resource, err := p.MapToFHIR(parsed, cls)
	if err != nil {
		t.Fatalf("MapToFHIR = %v, want nil", err)
	}
	var obs map[string]any
	if err := json.Unmarshal(resource.Body, &obs); err != nil {
		t.Fatalf("Body is not JSON: %v", err)
	}
	if got, _ := obs["valueString"].(string); got != "see note" {
		t.Errorf("valueString = %q, want see note", got)
	}
	if got, _ := obs["status"].(string); got != "amended" {
		t.Errorf("status = %q, want amended (OBX-11 = C)", got)
	}
	if _, ok := obs["valueQuantity"]; ok {
		t.Error("valueQuantity should not be present for string OBX-2")
	}
}

func TestDemoMapToFHIR_NonORURejected(t *testing.T) {
	t.Parallel()
	p := demoadapter.New().BuildHl7Processor(spi.AdapterContext{Now: time.Now})
	raw := []byte("MSH|^~\\&|MOCKEHR|E2E|FHIRSUBS|TEST|20260418101500||ADT^A01|MSG0003|T|2.5.1\rPID|1||PAT01^^^E2E^MR||Doe^Jane\r")
	parsed, err := p.Lex(raw)
	if err != nil {
		t.Fatalf("Lex = %v, want nil", err)
	}
	if _, err := p.Classify(parsed); err == nil {
		t.Error("Classify(ADT^A01) = nil, want error (demo adapter only translates ORU^R01)")
	}
}
