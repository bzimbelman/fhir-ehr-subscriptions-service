// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package channel_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/channel"
)

func TestDeliveryOutcomeKind(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		outcome channel.DeliveryOutcome
		want    channel.OutcomeKind
	}{
		{"delivered", channel.Delivered(), channel.OutcomeDelivered},
		{"transient", channel.TransientFailure(0, "5xx"), channel.OutcomeTransient},
		{"permanent", channel.PermanentFailure("404 not found"), channel.OutcomePermanent},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.outcome.Kind != tc.want {
				t.Errorf("kind = %v, want %v", tc.outcome.Kind, tc.want)
			}
		})
	}
}

func TestTransientFailureCarriesRetryAfter(t *testing.T) {
	t.Parallel()

	o := channel.TransientFailure(30*time.Second, "429 Too Many Requests")
	if o.Kind != channel.OutcomeTransient {
		t.Fatalf("kind = %v", o.Kind)
	}
	if o.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s", o.RetryAfter)
	}
	if o.Reason != "429 Too Many Requests" {
		t.Errorf("Reason = %q", o.Reason)
	}
}

func TestNotificationEnvelopeFields(t *testing.T) {
	t.Parallel()

	subID := uuid.New()
	corr := uuid.New().String()
	deadline := time.Now().Add(30 * time.Second)
	body := []byte(`{"resourceType":"Bundle"}`)

	env := channel.NotificationEnvelope{
		SubscriptionID:         subID,
		Sequence:               42,
		BundleBytes:            body,
		BundleKind:             channel.BundleEventNotification,
		PayloadType:            channel.PayloadIDOnly,
		ContentType:            channel.ContentTypeFHIRJSON,
		Attempt:                0,
		CorrelationID:          corr,
		SubscriptionEndpoint:   "https://subscriber.example/webhook",
		SubscriptionParameters: []channel.Param{{Name: "X-Tenant", Value: "acme"}},
		Deadline:               deadline,
	}

	if env.SubscriptionID != subID {
		t.Errorf("subID")
	}
	if env.Sequence != 42 {
		t.Errorf("seq")
	}
	if string(env.BundleBytes) != string(body) {
		t.Errorf("bundle")
	}
	if env.BundleKind != channel.BundleEventNotification {
		t.Errorf("kind")
	}
	if env.ContentType != channel.ContentTypeFHIRJSON {
		t.Errorf("ct")
	}
	if env.PayloadType != channel.PayloadIDOnly {
		t.Errorf("payload type")
	}
	if env.Attempt != 0 {
		t.Errorf("attempt")
	}
	if env.CorrelationID != corr {
		t.Errorf("corr id")
	}
	if env.SubscriptionEndpoint != "https://subscriber.example/webhook" {
		t.Errorf("endpoint")
	}
	if len(env.SubscriptionParameters) != 1 || env.SubscriptionParameters[0].Name != "X-Tenant" {
		t.Errorf("params")
	}
	if !env.Deadline.Equal(deadline) {
		t.Errorf("deadline")
	}
}
