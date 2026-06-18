// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

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

// TestE2E_S12_BundleSubSecondPrecision — the FHIR Bundle wire bytes
// retain sub-second precision after the builder fix.
func TestE2E_S12_BundleSubSecondPrecision(t *testing.T) {
	subID := uuid.New()
	evID := uuid.New()
	occurred := time.Date(2026, 1, 1, 12, 34, 56, 123000000, time.UTC)
	bldr := builder.New(builder.Config{Clock: func() time.Time { return occurred }})

	envelope, err := bldr.Build(context.Background(), builder.Job{
		Subscription: repos.SubscriptionRow{
			ID:          subID,
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
		t.Fatalf("Build: %v", err)
	}
	var bundle map[string]any
	if err := json.Unmarshal(envelope.BundleBytes, &bundle); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ts, _ := bundle["timestamp"].(string)
	if !strings.Contains(ts, ".123") {
		t.Errorf("Bundle timestamp lost precision: got %q", ts)
	}
}

// TestE2E_S12_HandshakeCorrelationDeterministic — handshake replays
// produce the same correlation_id.
func TestE2E_S12_HandshakeCorrelationDeterministic(t *testing.T) {
	subID := uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	bldr := builder.New(builder.Config{})
	job := builder.Job{
		Subscription: repos.SubscriptionRow{
			ID:          subID,
			ChannelType: "rest-hook",
			Endpoint:    "https://example.com",
			TopicURL:    "http://example/Topic/foo",
			Status:      repos.SubActive,
		},
		NotificationType: channel.BundleHandshake,
	}
	a, _ := bldr.Build(context.Background(), job)
	b, _ := bldr.Build(context.Background(), job)
	if a.CorrelationID != b.CorrelationID {
		t.Errorf("non-deterministic handshake correlation_id: %q vs %q",
			a.CorrelationID, b.CorrelationID)
	}
}
