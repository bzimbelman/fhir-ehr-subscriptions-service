// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package schemas_test

import (
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/schemas"
)

func TestValidateSubscription_HappyPath_R5Style(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.org/webhook",
		"content": "id-only",
		"channel": {"type": "rest-hook", "endpoint": "https://example.org/webhook"}
	}`)
	if err := schemas.ValidateSubscription(body); err != nil {
		t.Fatalf("expected valid; got %v", err)
	}
}

func TestValidateSubscription_HappyPath_R4BBackport(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"resourceType": "Subscription",
		"status": "requested",
		"criteria": "http://example.org/topics/orders",
		"channel": {
			"type": "rest-hook",
			"endpoint": "https://example.org/webhook",
			"payload": "application/fhir+json"
		}
	}`)
	if err := schemas.ValidateSubscription(body); err != nil {
		t.Fatalf("expected valid; got %v", err)
	}
}

func TestValidateSubscription_WrongResourceType(t *testing.T) {
	t.Parallel()
	body := []byte(`{"resourceType":"Patient","status":"requested","channel":{}}`)
	err := schemas.ValidateSubscription(body)
	if err == nil {
		t.Fatalf("expected error for wrong resourceType")
	}
}

func TestValidateSubscription_BadStatus(t *testing.T) {
	t.Parallel()
	body := []byte(`{"resourceType":"Subscription","status":"bogus","channel":{}}`)
	err := schemas.ValidateSubscription(body)
	if err == nil || !strings.Contains(err.Error(), "status") {
		t.Fatalf("expected error mentioning status; got %v", err)
	}
}

func TestValidateSubscription_BadEndpointURI(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"resourceType": "Subscription",
		"status": "requested",
		"channel": {"type": "rest-hook", "endpoint": "not a uri at all"}
	}`)
	if err := schemas.ValidateSubscription(body); err == nil {
		t.Fatalf("expected error for malformed endpoint URI")
	}
}

func TestValidateSubscription_MalformedJSON(t *testing.T) {
	t.Parallel()
	body := []byte(`{"resourceType":"Subscription"`)
	if err := schemas.ValidateSubscription(body); err == nil {
		t.Fatalf("expected JSON parse error")
	}
}

func TestValidateSubscription_BadContent(t *testing.T) {
	t.Parallel()
	body := []byte(`{"resourceType":"Subscription","status":"requested","content":"unknown","channel":{}}`)
	if err := schemas.ValidateSubscription(body); err == nil {
		t.Fatalf("expected error for invalid content enum")
	}
}
