// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package nextgenadapter_test

import (
	"bytes"
	"testing"
	"time"

	nextgenadapter "github.com/bzimbelman/fhir-ehr-subscriptions-service/adapters/nextgen"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

// TestNextGenMapToFHIRRoundTripsRealHL7 covers OP #171: NextGen MapToFHIR
// must use the parsed HL7 v2 input, not the hardcoded passthrough Bundle.
func TestNextGenMapToFHIRRoundTripsRealHL7(t *testing.T) {
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

	p := nextgenadapter.New().BuildHl7Processor(spi.AdapterContext{Now: time.Now})
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
					t.Errorf("Body missing %q.\n got: %s", want, res.Body)
				}
			}
			if bytes.Equal(res.Body, []byte(`{"resourceType":"Bundle","type":"collection"}`)) {
				t.Fatal("MapToFHIR returned the legacy hardcoded passthrough Bundle")
			}
		})
	}
}

func TestNextGenClassifyMapsADTTriggers(t *testing.T) {
	t.Parallel()
	p := nextgenadapter.New().BuildHl7Processor(spi.AdapterContext{Now: time.Now})
	cases := []struct {
		name string
		hl7  string
		want spi.ChangeKind
	}{
		{"A01_create", sampleADTA01, spi.ChangeCreate},
		{"A03_delete", "MSH|^~\\&|NEXTGEN|FAC|REC|FAC|20260618120000||ADT^A03|M|P|2.5\rPID|1||PATID9999\r", spi.ChangeDelete},
		{"A08_update", "MSH|^~\\&|NEXTGEN|FAC|REC|FAC|20260618120000||ADT^A08|M|P|2.5\rPID|1||PATID9999\r", spi.ChangeUpdate},
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

const sampleADTA01 = "MSH|^~\\&|NEXTGEN|FAC|REC|FAC|20260618120000||ADT^A01|MSG00001|P|2.5\r" +
	"EVN|A01|20260618120000\r" +
	"PID|1||PATID1234^5^M11^ADT1^MR^GOOD HEALTH HOSPITAL||EVERYWOMAN^EVE^E^^^^L||19620320|F\r" +
	"PV1|1|I|2000^2012^01\r"

const sampleORUR01 = "MSH|^~\\&|NEXTGEN|FAC|REC|FAC|20260618120000||ORU^R01|MSG00002|P|2.5\r" +
	"PID|1||PATID1234^5^M11^ADT1^MR^GOOD HEALTH HOSPITAL||EVERYWOMAN^EVE^E^^^^L||19620320|F\r" +
	"OBR|1|ORD-1|FILL-1|24323-8^Comprehensive Metabolic Panel^LN\r" +
	"OBX|1|NM|2951-2^Sodium^LN||140|mmol/L|136-145|N|||F\r"

const sampleORMO01 = "MSH|^~\\&|NEXTGEN|FAC|REC|FAC|20260618120000||ORM^O01|MSG00003|P|2.5\r" +
	"PID|1||PATID1234^5^M11^ADT1^MR^GOOD HEALTH HOSPITAL||EVERYWOMAN^EVE^E^^^^L||19620320|F\r" +
	"ORC|NW|ORD-1\r" +
	"OBR|1|ORD-1|FILL-1|24323-8^Comprehensive Metabolic Panel^LN\r"
