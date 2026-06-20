// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/demosupport/hl7"
)

// buildMessage turns a catalog entry into an HL7 v2 wire body using the
// internal/demosupport/hl7 builders. ctrlID is MSH-10 — the publisher
// mints a unique id per emission so the audience can correlate
// "→ send" with "← ACK".
//
// OP #158: this binary is operator-facing and intentionally avoids
// importing e2e/* packages so a future build tag on test scaffolding
// does not break the demo CLI build.
func buildMessage(e MessageEntry, ctrlID string) (body, ctrl string, err error) {
	patientID := e.Fields["patient_id"]
	switch e.Template {
	case "oru-r01":
		body = hl7.BuildORU(hl7.ORUOptions{
			MessageID: ctrlID,
			PatientID: patientID,
			Result: hl7.ORUResult{
				ObservationID: e.Fields["observation_code"],
				Value:         e.Fields["value"],
				Unit:          e.Fields["unit"],
				RefRange:      e.Fields["reference_range"],
				AbnormalFlag:  e.Fields["abnormal_flag"],
			},
		})
		return body, ctrlID, nil
	case "adt-a01":
		body = hl7.BuildADT(hl7.ADTOptions{
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
