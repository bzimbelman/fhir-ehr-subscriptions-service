// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package hl7v2

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

// MapToFHIR translates a parsed HL7 v2 message into a FHIR R4 transaction
// Bundle. Coverage:
//
//   - ADT^A01/A04/A08 -> Patient + Encounter
//   - ORU^R01         -> Patient + DiagnosticReport + Observation(s)
//   - ORM^O01         -> Patient + ServiceRequest
//
// Anything else falls back to a Patient-only Bundle so the framework's
// pipeline still gets a non-empty resource for the resource_changes
// outbox. Vendor adapters can wrap this and overlay vendor-specific
// extensions or Z-segment-driven fields.
//
// The mapping is intentionally minimal: enough to satisfy the AC ("uses
// parsed segments, not hardcoded bytes") and enough to round-trip in a
// FHIR-aware downstream. Operators who need richer profiles plug in a
// custom adapter.
func MapToFHIR(m *Message) (spi.FhirResource, error) {
	if m == nil {
		return spi.FhirResource{}, fmt.Errorf("hl7v2: nil message")
	}
	code := strings.ToUpper(m.MessageCode())
	trigger := strings.ToUpper(m.TriggerEvent())

	var entries []map[string]any
	patient := buildPatient(m)
	patientID := patient["id"].(string)
	entries = append(entries, bundleEntry(patient))

	switch code {
	case "ADT":
		switch trigger {
		case "A01", "A04", "A08", "A02", "A03", "A11":
			entries = append(entries, bundleEntry(buildEncounter(m, patientID)))
		}
	case "ORU":
		// DiagnosticReport summarizes the OBR; Observation per OBX.
		entries = append(entries, bundleEntry(buildDiagnosticReport(m, patientID)))
		for _, obx := range m.Segments("OBX") {
			entries = append(entries, bundleEntry(buildObservation(obx, patientID)))
		}
	case "ORM":
		entries = append(entries, bundleEntry(buildServiceRequest(m, patientID)))
	}

	bundle := map[string]any{
		"resourceType": "Bundle",
		"type":         "collection",
		"entry":        entries,
	}
	body, err := json.Marshal(bundle)
	if err != nil {
		return spi.FhirResource{}, fmt.Errorf("hl7v2: marshal bundle: %w", err)
	}
	return spi.FhirResource{
		ResourceType: "Bundle",
		Body:         body,
	}, nil
}

func bundleEntry(resource map[string]any) map[string]any {
	rt, _ := resource["resourceType"].(string)
	id, _ := resource["id"].(string)
	return map[string]any{
		"fullUrl":  fmt.Sprintf("urn:uuid:%s/%s", rt, id),
		"resource": resource,
	}
}

func buildPatient(m *Message) map[string]any {
	patient := map[string]any{"resourceType": "Patient"}
	pid, ok := m.Segment("PID")
	if !ok {
		patient["id"] = "unknown"
		return patient
	}

	// PID-3 is the patient identifier list. Use PID-3.1 as the resource id;
	// surface the full identifier as identifier[].value.
	id := pid.Component(3, 1)
	if id == "" {
		id = "unknown"
	}
	patient["id"] = id
	patient["identifier"] = []map[string]any{{
		"system": "urn:fhir-subs:hl7v2:pid-3",
		"value":  id,
	}}

	// PID-5 carries the patient name in XPN form: family^given^middle^...^suffix.
	if family := pid.Component(5, 1); family != "" {
		name := map[string]any{"family": family}
		if given := pid.Component(5, 2); given != "" {
			name["given"] = []string{given}
		}
		patient["name"] = []map[string]any{name}
	}
	if dob := pid.Field(7); dob != "" {
		patient["birthDate"] = formatDate(dob)
	}
	if sex := pid.Field(8); sex != "" {
		patient["gender"] = mapGender(sex)
	}
	return patient
}

func buildEncounter(m *Message, patientID string) map[string]any {
	enc := map[string]any{
		"resourceType": "Encounter",
		"id":           "enc-" + patientID,
		"status":       "in-progress",
		"subject":      map[string]any{"reference": "Patient/" + patientID},
	}
	pv1, ok := m.Segment("PV1")
	if !ok {
		return enc
	}
	if class := pv1.Field(2); class != "" {
		enc["class"] = map[string]any{
			"system":  "http://terminology.hl7.org/CodeSystem/v3-ActCode",
			"code":    class,
			"display": classDisplay(class),
		}
	}
	if visitID := pv1.Component(19, 1); visitID != "" {
		enc["identifier"] = []map[string]any{{
			"system": "urn:fhir-subs:hl7v2:pv1-19",
			"value":  visitID,
		}}
	}
	return enc
}

func buildDiagnosticReport(m *Message, patientID string) map[string]any {
	report := map[string]any{
		"resourceType": "DiagnosticReport",
		"id":           "rpt-" + patientID,
		"status":       "final",
		"subject":      map[string]any{"reference": "Patient/" + patientID},
	}
	if obr, ok := m.Segment("OBR"); ok {
		if id := obr.Field(2); id != "" {
			report["identifier"] = []map[string]any{{
				"system": "urn:fhir-subs:hl7v2:obr-2",
				"value":  id,
			}}
		}
		if code := obr.Component(4, 1); code != "" {
			report["code"] = map[string]any{
				"coding": []map[string]any{{
					"code":    code,
					"display": obr.Component(4, 2),
				}},
				"text": obr.Component(4, 2),
			}
		}
	}
	return report
}

func buildObservation(obx Segment, patientID string) map[string]any {
	obs := map[string]any{
		"resourceType": "Observation",
		"id":           "obs-" + patientID + "-" + obx.Field(1),
		"status":       "final",
		"subject":      map[string]any{"reference": "Patient/" + patientID},
	}
	if code := obx.Component(3, 1); code != "" {
		obs["code"] = map[string]any{
			"coding": []map[string]any{{
				"code":    code,
				"display": obx.Component(3, 2),
			}},
			"text": obx.Component(3, 2),
		}
	}
	if value := obx.Field(5); value != "" {
		obs["valueString"] = value
	}
	if unit := obx.Field(6); unit != "" {
		obs["valueQuantity"] = map[string]any{
			"value": obx.Field(5),
			"unit":  unit,
		}
		// Drop valueString when valueQuantity is present (FHIR R4: choice type).
		delete(obs, "valueString")
	}
	return obs
}

func buildServiceRequest(m *Message, patientID string) map[string]any {
	sr := map[string]any{
		"resourceType": "ServiceRequest",
		"id":           "sr-" + patientID,
		"status":       "active",
		"intent":       "order",
		"subject":      map[string]any{"reference": "Patient/" + patientID},
	}
	if orc, ok := m.Segment("ORC"); ok {
		if order := orc.Field(2); order != "" {
			sr["identifier"] = []map[string]any{{
				"system": "urn:fhir-subs:hl7v2:orc-2",
				"value":  order,
			}}
			sr["id"] = "sr-" + order
		}
	}
	if obr, ok := m.Segment("OBR"); ok {
		if code := obr.Component(4, 1); code != "" {
			sr["code"] = map[string]any{
				"coding": []map[string]any{{
					"code":    code,
					"display": obr.Component(4, 2),
				}},
				"text": obr.Component(4, 2),
			}
		}
	}
	return sr
}

// mapGender maps the HL7 admin sex to FHIR AdministrativeGender.
func mapGender(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "M":
		return "male"
	case "F":
		return "female"
	case "O":
		return "other"
	case "U", "":
		return "unknown"
	}
	return "unknown"
}

// formatDate converts an HL7 v2 timestamp (YYYYMMDD or YYYYMMDDHHMMSS) into
// a FHIR date (YYYY-MM-DD). Anything we don't recognize is returned as-is so
// downstream validators can complain meaningfully.
func formatDate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 8 {
		return s
	}
	return s[:4] + "-" + s[4:6] + "-" + s[6:8]
}

// classDisplay maps a HL7 v2 PV1-2 patient class code to a human label.
func classDisplay(code string) string {
	switch strings.ToUpper(code) {
	case "I":
		return "inpatient"
	case "O":
		return "outpatient"
	case "E":
		return "emergency"
	case "P":
		return "preadmit"
	case "R":
		return "recurring"
	}
	return code
}
