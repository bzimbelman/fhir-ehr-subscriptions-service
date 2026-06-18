// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/topics/catalog"
)

// TestMatcher_unknownParamRejected (B-23): a topic that references a
// FHIR search parameter outside the matcher's supported subset must be
// rejected at catalog load time. Previously such topics passed schema
// validation and silently never matched at run time.
func TestMatcher_unknownParamRejected(t *testing.T) {
	const badTopic = `{
		"resourceType": "SubscriptionTopic",
		"url": "http://example.org/topics/performer-driven",
		"version": "1.0.0",
		"status": "active",
		"resourceTrigger": [{
			"resource": "ServiceRequest",
			"supportedInteraction": ["create", "update"],
			"queryCriteria": {
				"current": "performer=Practitioner/123"
			}
		}]
	}`

	report, err := catalog.Load(catalog.Sources{
		Operator: []catalog.RawTopic{
			{Origin: "/etc/topics/performer.json", Bytes: []byte(badTopic)},
		},
	})
	if err != nil {
		t.Fatalf("Load returned err: %v", err)
	}
	if got := len(report.Rejected); got != 1 {
		t.Fatalf("expected 1 rejected topic, got %d (%#v)", got, report.Rejected)
	}
	r := report.Rejected[0]
	if !strings.Contains(r.Reason, "performer") {
		t.Errorf("rejection reason should name the offending parameter; got %q", r.Reason)
	}
	if got := len(report.Catalog.All()); got != 0 {
		t.Errorf("rejected topic must NOT appear in Catalog.All(); got %d topics", got)
	}
	cat := report.Catalog
	if got := len(cat.Rejected()); got != 1 {
		t.Errorf("Catalog.Rejected() should mirror Report.Rejected; got %d entries", got)
	}
}
