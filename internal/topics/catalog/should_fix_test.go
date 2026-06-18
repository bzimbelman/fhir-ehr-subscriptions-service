// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// SHOULD-FIX coverage for S-11 audit findings.

package catalog

import (
	"strings"
	"testing"
)

// TestS11_1_RejectsUnknownSupportedInteraction — S-11.1: typos in
// supportedInteraction were silently passed through (e.g., "createt"
// would never match anything). Now the catalog rejects unknown
// values at compile time.
func TestS11_1_RejectsUnknownSupportedInteraction(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"resourceType": "SubscriptionTopic",
		"url": "http://example/Topic/typo",
		"version": "1.0",
		"status": "active",
		"resourceTrigger": [
			{
				"description": "x",
				"resource": "Patient",
				"supportedInteraction": ["createt"]
			}
		]
	}`)
	rep, err := Load(Sources{Operator: []RawTopic{{Origin: "test", Bytes: body}}})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rep.Rejected) == 0 {
		t.Errorf("typo'd supportedInteraction must be rejected")
	}
	found := false
	for _, r := range rep.Rejected {
		if strings.Contains(r.Reason, "supportedInteraction") {
			found = true
		}
	}
	if !found {
		t.Errorf("rejection reason should name supportedInteraction; got %+v", rep.Rejected)
	}
}

// TestS11_1_AcceptsCanonicalInteractions — sanity: create/update/delete
// still pass.
func TestS11_1_AcceptsCanonicalInteractions(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"resourceType": "SubscriptionTopic",
		"url": "http://example/Topic/ok",
		"version": "1.0",
		"status": "active",
		"resourceTrigger": [
			{
				"description": "x",
				"resource": "Patient",
				"supportedInteraction": ["create", "update", "delete"]
			}
		]
	}`)
	rep, err := Load(Sources{Operator: []RawTopic{{Origin: "test", Bytes: body}}})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rep.Rejected) != 0 {
		t.Errorf("canonical interactions should not be rejected: %+v", rep.Rejected)
	}
}
