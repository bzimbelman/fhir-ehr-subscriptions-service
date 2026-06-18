// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/topics/catalog"
)

// TestMatcher_strictMode (B-25): in strict-mode startup, ANY topic
// failure must be fatal — the operator wants the process to refuse
// to start with a broken catalog rather than silently lose topics.
func TestMatcher_strictMode(t *testing.T) {
	const broken = `{
		"resourceType": "SubscriptionTopic",
		"url": "http://example.org/topics/broken",
		"version": "1.0.0",
		"status": "active",
		"resourceTrigger": [{
			"resource": "ServiceRequest",
			"supportedInteraction": ["create"],
			"queryCriteria": {
				"current": "performer=Practitioner/123"
			}
		}]
	}`

	rep, err := catalog.LoadStrict(catalog.Sources{
		Operator: []catalog.RawTopic{
			{Origin: "/etc/topics/broken.json", Bytes: []byte(broken)},
		},
	})
	if err == nil {
		t.Fatal("LoadStrict must return a non-nil error when any topic is rejected")
	}
	if !strings.Contains(err.Error(), "performer") {
		t.Errorf("strict-mode error should name the offending parameter; got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "/etc/topics/broken.json") {
		t.Errorf("strict-mode error should name the offending origin so an operator can grep their config; got %q", err.Error())
	}
	if rep.Catalog == nil {
		t.Error("LoadStrict should still return a Catalog handle for diagnostics")
	}
}
