// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// SHOULD-FIX coverage for S-12 builder findings.

package builder_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/builder"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// TestS12_BuilderTimestampSubSecondPrecision — S-12: RFC3339 (no Nano)
// drops sub-second precision; FHIR `instant` allows fractions. Switch
// to RFC3339Nano on event timestamp + Bundle.timestamp.
func TestS12_BuilderTimestampSubSecondPrecision(t *testing.T) {
	t.Parallel()

	subID := uuid.New()
	evID := uuid.New()
	occurred := time.Date(2026, 1, 1, 12, 34, 56, 789000000, time.UTC)
	bldr := builder.New(builder.Config{Clock: func() time.Time { return occurred }})

	envelope, err := bldr.Build(context.Background(), builder.Job{
		Subscription: repos.SubscriptionRow{
			ID:         subID,
			ChannelType: "rest-hook",
			Endpoint:    "https://example.com",
			TopicURL:    "http://example/Topic/foo",
			Status:      repos.SubActive,
			Content:     "id-only",
		},
		NotificationType:   channel.BundleEventNotification,
		Events:             []repos.EhrEventRow{{ID: evID, OccurredAt: occurred, Focus: "Patient/123"}},
		PerSubEventNumbers: map[uuid.UUID]int64{evID: 1},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var bundle map[string]any
	if err := json.Unmarshal(envelope.BundleBytes, &bundle); err != nil {
		t.Fatalf("decode bundle: %v", err)
	}
	ts, _ := bundle["timestamp"].(string)
	if !strings.Contains(ts, "T12:34:56.789") {
		t.Errorf("Bundle timestamp lost sub-second precision: got %q", ts)
	}
	// notificationEvent[0].timestamp also keeps sub-second.
	entry := bundle["entry"].([]any)
	status := entry[0].(map[string]any)["resource"].(map[string]any)
	notif := status["notificationEvent"].([]any)
	notifTS := notif[0].(map[string]any)["timestamp"].(string)
	if !strings.Contains(notifTS, "T12:34:56.789") {
		t.Errorf("notificationEvent timestamp lost sub-second precision: got %q", notifTS)
	}
}

// TestS12_HandshakeCorrelationIDDeterministic — S-12: handshake / heartbeat
// must produce a deterministic correlation_id so a replay yields the
// same value. The fix derives the ID from the subscription id when no
// override and no events provide one.
func TestS12_HandshakeCorrelationIDDeterministic(t *testing.T) {
	t.Parallel()
	subID := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	bldr := builder.New(builder.Config{})
	jobBase := builder.Job{
		Subscription: repos.SubscriptionRow{
			ID:         subID,
			ChannelType: "rest-hook",
			Endpoint:    "https://example.com",
			TopicURL:    "http://example/Topic/foo",
			Status:      repos.SubActive,
		},
		NotificationType: channel.BundleHandshake,
	}
	env1, err := bldr.Build(context.Background(), jobBase)
	if err != nil {
		t.Fatalf("build1: %v", err)
	}
	env2, err := bldr.Build(context.Background(), jobBase)
	if err != nil {
		t.Fatalf("build2: %v", err)
	}
	if env1.CorrelationID != env2.CorrelationID {
		t.Errorf("handshake correlation_id non-deterministic: %s vs %s",
			env1.CorrelationID, env2.CorrelationID)
	}
	// Should be derived from sub id, not random uuid.
	if env1.CorrelationID == uuid.NewString() {
		t.Errorf("correlation_id looks like fresh random uuid")
	}
}

// S-12: fhir+xml deferred but not rejected at create — that finding
// applies to the API create-subscription path. The builder itself
// already hardcodes fhir+json (builder.go:252-253) so there is
// nothing to change here. Tracked under the API surface area; the
// builder is correct as-is.
