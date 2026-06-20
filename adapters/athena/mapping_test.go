// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package athenaadapter_test

import (
	"bytes"
	"testing"
	"time"

	athenaadapter "github.com/bzimbelman/fhir-ehr-subscriptions-service/adapters/athena"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

// TestAthenaMapToFHIRRoundTripsRealHL7 — see cerner/mapping_test.go for the
// full rationale. Skipped until OP #170 (Athena MapToFHIR implementation)
// lands.
func TestAthenaMapToFHIRRoundTripsRealHL7(t *testing.T) {
	t.Parallel()
	t.Skip("blocked on OP #170 (implement Athena MapToFHIR HL7 v2 -> FHIR R4 mapping)")

	cases := []struct {
		name             string
		hl7              string
		wantResourceType string
	}{
		{"ADT_A01", sampleADTA01, "Bundle"},
		{"ORU_R01", sampleORUR01, "Bundle"},
		{"ORM_O01", sampleORMO01, "Bundle"},
	}

	p := athenaadapter.New().BuildHl7Processor(spi.AdapterContext{Now: time.Now})
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
			if !bytes.Contains(res.Body, []byte("PATID1234")) {
				t.Errorf("Body missing patient identifier PATID1234 — MapToFHIR ignored its input.\n got: %s", res.Body)
			}
		})
	}
}

const sampleADTA01 = "MSH|^~\\&|ATHENA|FAC|REC|FAC|20260618120000||ADT^A01|MSG00001|P|2.5\r" +
	"EVN|A01|20260618120000\r" +
	"PID|1||PATID1234^5^M11^ADT1^MR^GOOD HEALTH HOSPITAL||EVERYWOMAN^EVE^E^^^^L||19620320|F\r" +
	"PV1|1|I|2000^2012^01\r"

const sampleORUR01 = "MSH|^~\\&|ATHENA|FAC|REC|FAC|20260618120000||ORU^R01|MSG00002|P|2.5\r" +
	"PID|1||PATID1234^5^M11^ADT1^MR^GOOD HEALTH HOSPITAL||EVERYWOMAN^EVE^E^^^^L||19620320|F\r" +
	"OBR|1|ORD-1|FILL-1|24323-8^Comprehensive Metabolic Panel^LN\r" +
	"OBX|1|NM|2951-2^Sodium^LN||140|mmol/L|136-145|N|||F\r"

const sampleORMO01 = "MSH|^~\\&|ATHENA|FAC|REC|FAC|20260618120000||ORM^O01|MSG00003|P|2.5\r" +
	"PID|1||PATID1234^5^M11^ADT1^MR^GOOD HEALTH HOSPITAL||EVERYWOMAN^EVE^E^^^^L||19620320|F\r" +
	"ORC|NW|ORD-1\r" +
	"OBR|1|ORD-1|FILL-1|24323-8^Comprehensive Metabolic Panel^LN\r"
