// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package cerneradapter_test

import (
	"bytes"
	"testing"
	"time"

	cerneradapter "github.com/bzimbelman/fhir-ehr-subscriptions-service/adapters/cerner"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

// TestCernerMapToFHIRRoundTripsRealHL7 covers OP #168: Cerner MapToFHIR
// must use the parsed HL7 v2 input, not the hardcoded
// `{"resourceType":"Bundle","type":"collection"}` stub the scaffold
// shipped with.
//
// The test loads real-shape HL7 v2 messages (ADT^A01, ORU^R01, ORM^O01),
// runs Lex + Classify + MapToFHIR, and asserts:
//   - ResourceType is "Bundle"
//   - Body carries the PATID1234 identifier from PID-3.1
//   - Body carries an ADT^A01 -> Patient + Encounter, ORU^R01 ->
//     DiagnosticReport + Observation(s), ORM^O01 -> ServiceRequest
//     resource as appropriate.
func TestCernerMapToFHIRRoundTripsRealHL7(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name             string
		hl7              string
		wantResourceType string
		wantContains     []string
	}{
		{"ADT_A01", sampleADTA01, "Bundle", []string{"PATID1234", "Patient", "Encounter"}},
		{"ORU_R01", sampleORUR01, "Bundle", []string{"PATID1234", "DiagnosticReport", "Observation", "Sodium"}},
		{"ORM_O01", sampleORMO01, "Bundle", []string{"PATID1234", "ServiceRequest", "ORD-1"}},
	}

	p := cerneradapter.New().BuildHl7Processor(spi.AdapterContext{Now: time.Now})
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parsed, err := p.Lex([]byte(tc.hl7))
			if err != nil {
				t.Fatalf("Lex: %v", err)
			}
			class, err := p.Classify(parsed)
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			res, err := p.MapToFHIR(parsed, class)
			if err != nil {
				t.Fatalf("MapToFHIR: %v", err)
			}
			if res.ResourceType != tc.wantResourceType {
				t.Errorf("ResourceType = %q, want %q", res.ResourceType, tc.wantResourceType)
			}
			if len(res.Body) == 0 {
				t.Fatal("Body is empty; MapToFHIR produced no payload")
			}
			for _, want := range tc.wantContains {
				if !bytes.Contains(res.Body, []byte(want)) {
					t.Errorf("Body missing %q — MapToFHIR ignored its input.\n got: %s",
						want, res.Body)
				}
			}
			if bytes.Equal(res.Body, []byte(`{"resourceType":"Bundle","type":"collection"}`)) {
				t.Fatal("MapToFHIR returned the legacy hardcoded passthrough Bundle")
			}
		})
	}
}

func TestCernerClassifyMapsADTTriggers(t *testing.T) {
	t.Parallel()
	p := cerneradapter.New().BuildHl7Processor(spi.AdapterContext{Now: time.Now})
	cases := []struct {
		name string
		hl7  string
		want spi.ChangeKind
	}{
		{"A01_create", sampleADTA01, spi.ChangeCreate},
		{"A03_delete", "MSH|^~\\&|CERNER|FAC|REC|FAC|20260618120000||ADT^A03|M|P|2.5\rPID|1||PATID9999\r", spi.ChangeDelete},
		{"A08_update", "MSH|^~\\&|CERNER|FAC|REC|FAC|20260618120000||ADT^A08|M|P|2.5\rPID|1||PATID9999\r", spi.ChangeUpdate},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parsed, err := p.Lex([]byte(tc.hl7))
			if err != nil {
				t.Fatalf("Lex: %v", err)
			}
			c, err := p.Classify(parsed)
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			if c.Kind != tc.want {
				t.Errorf("Kind = %q, want %q", c.Kind, tc.want)
			}
		})
	}
}

// Real-shape HL7 v2 fixtures shared across adapter mapping tests. PATID1234
// is the load-bearing token: any genuine MapToFHIR implementation that uses
// the parsed input must surface this identifier in the resulting Bundle.

const sampleADTA01 = "MSH|^~\\&|CERNER|FAC|REC|FAC|20260618120000||ADT^A01|MSG00001|P|2.5\r" +
	"EVN|A01|20260618120000\r" +
	"PID|1||PATID1234^5^M11^ADT1^MR^GOOD HEALTH HOSPITAL||EVERYWOMAN^EVE^E^^^^L||19620320|F\r" +
	"PV1|1|I|2000^2012^01\r"

const sampleORUR01 = "MSH|^~\\&|CERNER|FAC|REC|FAC|20260618120000||ORU^R01|MSG00002|P|2.5\r" +
	"PID|1||PATID1234^5^M11^ADT1^MR^GOOD HEALTH HOSPITAL||EVERYWOMAN^EVE^E^^^^L||19620320|F\r" +
	"OBR|1|ORD-1|FILL-1|24323-8^Comprehensive Metabolic Panel^LN\r" +
	"OBX|1|NM|2951-2^Sodium^LN||140|mmol/L|136-145|N|||F\r"

const sampleORMO01 = "MSH|^~\\&|CERNER|FAC|REC|FAC|20260618120000||ORM^O01|MSG00003|P|2.5\r" +
	"PID|1||PATID1234^5^M11^ADT1^MR^GOOD HEALTH HOSPITAL||EVERYWOMAN^EVE^E^^^^L||19620320|F\r" +
	"ORC|NW|ORD-1\r" +
	"OBR|1|ORD-1|FILL-1|24323-8^Comprehensive Metabolic Panel^LN\r"
