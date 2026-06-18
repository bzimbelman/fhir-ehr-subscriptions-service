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

// TestS11_3_RejectsMultiEntryNotificationShape — S-11.3: a topic
// authored with multiple notificationShape entries used to silently
// collapse to a single shape, picking the last entry's resource and
// concatenating includes/revIncludes across entries. That hides a
// real spec divergence: per-resource shape selection requires
// per-entry compile, which v1 does not support. Reject at load so
// operators see the failure during deploy rather than receiving
// incorrect Bundles at runtime.
func TestS11_3_RejectsMultiEntryNotificationShape(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"resourceType": "SubscriptionTopic",
		"url": "http://example/Topic/multi-shape",
		"version": "1.0",
		"status": "active",
		"resourceTrigger": [
			{
				"description": "x",
				"resource": "ServiceRequest",
				"supportedInteraction": ["create"]
			}
		],
		"notificationShape": [
			{"resource": "ServiceRequest", "include": ["ServiceRequest:patient"]},
			{"resource": "Observation",    "include": ["Observation:subject"]}
		]
	}`)
	rep, err := Load(Sources{Operator: []RawTopic{{Origin: "test/multi", Bytes: body}}})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rep.Rejected) != 1 {
		t.Fatalf("expected 1 rejection, got %d: %+v", len(rep.Rejected), rep.Rejected)
	}
	r := rep.Rejected[0]
	if r.URL != "http://example/Topic/multi-shape" {
		t.Errorf("rejection URL mismatch: %q", r.URL)
	}
	if !strings.Contains(r.Reason, "multi-entry notificationShape") {
		t.Errorf("rejection reason should mention 'multi-entry notificationShape'; got %q", r.Reason)
	}
	if !strings.Contains(r.Reason, "http://example/Topic/multi-shape") {
		t.Errorf("rejection reason should include the topic URL; got %q", r.Reason)
	}
	if rep.Catalog.Get("http://example/Topic/multi-shape") != nil {
		t.Errorf("rejected topic must not appear in the catalog")
	}
}

// TestS11_3_AcceptsSingleEntryNotificationShape — sanity: the
// canonical single-entry case (and the empty case) still load
// successfully after the multi-entry rejection lands.
func TestS11_3_AcceptsSingleEntryNotificationShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{
			name: "single-entry",
			body: `{
				"resourceType": "SubscriptionTopic",
				"url": "http://example/Topic/single-shape",
				"version": "1.0",
				"status": "active",
				"resourceTrigger": [
					{"description": "x", "resource": "ServiceRequest", "supportedInteraction": ["create"]}
				],
				"notificationShape": [
					{"resource": "ServiceRequest", "include": ["ServiceRequest:patient"]}
				]
			}`,
		},
		{
			name: "no-shape",
			body: `{
				"resourceType": "SubscriptionTopic",
				"url": "http://example/Topic/no-shape",
				"version": "1.0",
				"status": "active",
				"resourceTrigger": [
					{"description": "x", "resource": "ServiceRequest", "supportedInteraction": ["create"]}
				]
			}`,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rep, err := Load(Sources{Operator: []RawTopic{{Origin: "test/" + tc.name, Bytes: []byte(tc.body)}}})
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if len(rep.Rejected) != 0 {
				t.Fatalf("expected no rejections, got: %+v", rep.Rejected)
			}
			if len(rep.Catalog.All()) != 1 {
				t.Fatalf("expected 1 topic in catalog, got %d", len(rep.Catalog.All()))
			}
		})
	}
}
