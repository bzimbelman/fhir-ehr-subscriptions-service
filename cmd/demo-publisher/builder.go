// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/mockehr"
)

// buildMessage turns a catalog entry into an HL7 v2 wire body using the
// e2e/mockehr builders. ctrlID is MSH-10 — the publisher mints a unique
// id per emission so the audience can correlate "→ send" with "← ACK".
//
// Returns the raw HL7 string, the control id used, and an error if the
// template is unknown (catalog validation should prevent that path).
func buildMessage(e MessageEntry, ctrlID string) (body string, ctrl string, err error) {
	patientID := e.Fields["patient_id"]
	switch e.Template {
	case "oru-r01":
		body = mockehr.BuildORU(mockehr.ORUOptions{
			MessageID: ctrlID,
			PatientID: patientID,
			Result: mockehr.ORUResult{
				ObservationID: e.Fields["observation_code"],
				Value:         e.Fields["value"],
				Unit:          e.Fields["unit"],
				RefRange:      e.Fields["reference_range"],
				AbnormalFlag:  e.Fields["abnormal_flag"],
			},
		})
		return body, ctrlID, nil
	case "adt-a01":
		body = mockehr.BuildADT(mockehr.ADTOptions{
			TriggerEvent:  "A01",
			MessageID:     ctrlID,
			PatientID:     patientID,
			PatientFamily: e.Fields["family_name"],
			PatientGiven:  e.Fields["given_name"],
		})
		return body, ctrlID, nil
	default:
		return "", "", fmt.Errorf("buildMessage: unknown template %q", e.Template)
	}
}

// triggerLabel returns the MSH-9 token used in the printed "→" line so
// operators see "ORU^R01" / "ADT^A01" rather than the catalog template id.
func triggerLabel(template string) string {
	switch template {
	case "oru-r01":
		return "ORU^R01"
	case "adt-a01":
		return "ADT^A01"
	default:
		return template
	}
}
