// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package builder_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/channel"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/engine/builder"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/storage/repos"
)

// TestBuildHandshakeStatusOnly: a handshake notification is a
// SubscriptionStatus-only Bundle (no notificationEvent, no body
// resources) with type "handshake".
func TestBuildHandshakeStatusOnly(t *testing.T) {
	t.Parallel()
	sub := repos.SubscriptionRow{
		ID:          uuid.New(),
		Status:      repos.SubRequested,
		TopicURL:    "http://example.org/order-changed",
		ChannelType: "rest-hook",
		Endpoint:    "https://sub.example.org/notif",
		Content:     "id-only",
	}
	b := builder.New(builder.Config{})
	env, err := b.Build(context.Background(), builder.Job{
		Subscription:     sub,
		NotificationType: channel.BundleHandshake,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if env.BundleKind != channel.BundleHandshake {
		t.Errorf("BundleKind: got %v, want handshake", env.BundleKind)
	}
	if env.ContentType != channel.ContentTypeFHIRJSON {
		t.Errorf("ContentType: got %v, want fhir+json", env.ContentType)
	}

	bundle := unmarshalBundle(t, env.BundleBytes)
	if got := bundle["resourceType"]; got != "Bundle" {
		t.Errorf("resourceType: got %v", got)
	}
	if got := bundle["type"]; got != "subscription-notification" {
		t.Errorf("Bundle.type: got %v", got)
	}
	entries := bundle["entry"].([]any)
	if len(entries) != 1 {
		t.Fatalf("handshake Bundle should have exactly one entry (SubscriptionStatus), got %d", len(entries))
	}
	status := entries[0].(map[string]any)["resource"].(map[string]any)
	if status["resourceType"] != "SubscriptionStatus" {
		t.Errorf("entry[0]: not a SubscriptionStatus")
	}
	if status["type"] != "handshake" {
		t.Errorf("SubscriptionStatus.type: got %v", status["type"])
	}
	if _, ok := status["notificationEvent"]; ok {
		t.Errorf("handshake should have no notificationEvent")
	}
}

// TestBuildHeartbeatStatusOnly: heartbeat is structurally identical to
// handshake but with type "heartbeat".
func TestBuildHeartbeatStatusOnly(t *testing.T) {
	t.Parallel()
	sub := repos.SubscriptionRow{
		ID:       uuid.New(),
		Status:   repos.SubActive,
		TopicURL: "http://example.org/t",
		Content:  "id-only",
	}
	b := builder.New(builder.Config{})
	env, err := b.Build(context.Background(), builder.Job{
		Subscription:     sub,
		NotificationType: channel.BundleHeartbeat,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	bundle := unmarshalBundle(t, env.BundleBytes)
	entries := bundle["entry"].([]any)
	if len(entries) != 1 {
		t.Fatalf("heartbeat Bundle should have exactly one entry, got %d", len(entries))
	}
	status := entries[0].(map[string]any)["resource"].(map[string]any)
	if status["type"] != "heartbeat" {
		t.Errorf("type: got %v", status["type"])
	}
}

// TestBuildEmptyPayloadEventOmitsFocus: payload "empty" → notificationEvent
// is present, eventNumber+timestamp populated, but focus must be absent.
// No resource bodies in the Bundle.
func TestBuildEmptyPayloadEventOmitsFocus(t *testing.T) {
	t.Parallel()
	sub := repos.SubscriptionRow{
		ID: uuid.New(), TopicURL: "http://example.org/t", Status: repos.SubActive,
		Content: "empty",
	}
	ev := repos.EhrEventRow{
		ID:               uuid.New(),
		EventNumber:      77,
		TopicURL:         "http://example.org/t",
		Focus:            "ServiceRequest/abc",
		ChangeKind:       repos.ChangeCreate,
		Resource:         []byte(`{"resourceType":"ServiceRequest","id":"abc"}`),
		CorrelationID:    uuid.New(),
		OccurredAt:       time.Now().UTC(),
		ResourceChangeID: uuid.New(),
	}
	b := builder.New(builder.Config{})
	env, err := b.Build(context.Background(), builder.Job{
		Subscription:     sub,
		NotificationType: channel.BundleEventNotification,
		Events:           []repos.EhrEventRow{ev},
		PerSubEventNumbers: map[uuid.UUID]int64{
			ev.ID: 1,
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	bundle := unmarshalBundle(t, env.BundleBytes)
	entries := bundle["entry"].([]any)
	if len(entries) != 1 {
		t.Fatalf("empty payload event Bundle should have exactly one entry (status only), got %d", len(entries))
	}
	status := entries[0].(map[string]any)["resource"].(map[string]any)
	notifEvents, ok := status["notificationEvent"].([]any)
	if !ok || len(notifEvents) != 1 {
		t.Fatalf("expected one notificationEvent")
	}
	notif := notifEvents[0].(map[string]any)
	if _, hasFocus := notif["focus"]; hasFocus {
		t.Errorf("empty payload: notificationEvent[0].focus must be absent")
	}
	if got, _ := notif["eventNumber"].(float64); int64(got) != 1 {
		t.Errorf("eventNumber: got %v", notif["eventNumber"])
	}
}

// TestBuildIDOnlyPayloadHasFocusReferenceNoBodies: payload "id-only" →
// notificationEvent has focus reference, no resource bodies.
func TestBuildIDOnlyPayloadHasFocusReferenceNoBodies(t *testing.T) {
	t.Parallel()
	sub := repos.SubscriptionRow{
		ID: uuid.New(), TopicURL: "http://example.org/t", Status: repos.SubActive,
		Content: "id-only",
	}
	ev := repos.EhrEventRow{
		ID:               uuid.New(),
		EventNumber:      9001,
		TopicURL:         "http://example.org/t",
		Focus:            "ServiceRequest/srA",
		ChangeKind:       repos.ChangeUpdate,
		Resource:         []byte(`{"resourceType":"ServiceRequest","id":"srA","status":"active"}`),
		CorrelationID:    uuid.New(),
		OccurredAt:       time.Now().UTC(),
		ResourceChangeID: uuid.New(),
	}
	b := builder.New(builder.Config{})
	env, err := b.Build(context.Background(), builder.Job{
		Subscription:       sub,
		NotificationType:   channel.BundleEventNotification,
		Events:             []repos.EhrEventRow{ev},
		PerSubEventNumbers: map[uuid.UUID]int64{ev.ID: 1},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	bundle := unmarshalBundle(t, env.BundleBytes)
	entries := bundle["entry"].([]any)
	// id-only must have status only — no bodies.
	if len(entries) != 1 {
		t.Fatalf("id-only event Bundle: want 1 entry (status), got %d", len(entries))
	}
	status := entries[0].(map[string]any)["resource"].(map[string]any)
	notifEvents := status["notificationEvent"].([]any)
	notif := notifEvents[0].(map[string]any)
	focus, ok := notif["focus"].(map[string]any)
	if !ok {
		t.Fatalf("id-only: notificationEvent[0].focus must be a Reference object")
	}
	if got, _ := focus["reference"].(string); got != "ServiceRequest/srA" {
		t.Errorf("focus.reference: got %v", focus["reference"])
	}
}

// TestBuildFullResourcePayloadIncludesFocusBody: payload "full-resource"
// → notificationEvent has focus reference AND the focus resource appears
// as a body entry after the SubscriptionStatus.
func TestBuildFullResourcePayloadIncludesFocusBody(t *testing.T) {
	t.Parallel()
	sub := repos.SubscriptionRow{
		ID: uuid.New(), TopicURL: "http://example.org/t", Status: repos.SubActive,
		Content: "full-resource",
	}
	ev := repos.EhrEventRow{
		ID:               uuid.New(),
		EventNumber:      42,
		TopicURL:         "http://example.org/t",
		Focus:            "ServiceRequest/srF",
		ChangeKind:       repos.ChangeUpdate,
		Resource:         []byte(`{"resourceType":"ServiceRequest","id":"srF","status":"active"}`),
		CorrelationID:    uuid.New(),
		OccurredAt:       time.Now().UTC(),
		ResourceChangeID: uuid.New(),
	}
	b := builder.New(builder.Config{})
	env, err := b.Build(context.Background(), builder.Job{
		Subscription:       sub,
		NotificationType:   channel.BundleEventNotification,
		Events:             []repos.EhrEventRow{ev},
		PerSubEventNumbers: map[uuid.UUID]int64{ev.ID: 1},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	bundle := unmarshalBundle(t, env.BundleBytes)
	entries := bundle["entry"].([]any)
	if len(entries) != 2 {
		t.Fatalf("full-resource: want 2 entries (status + focus body), got %d", len(entries))
	}
	body := entries[1].(map[string]any)["resource"].(map[string]any)
	if body["resourceType"] != "ServiceRequest" || body["id"] != "srF" {
		t.Errorf("body: got %#v", body)
	}
}

// TestBuildBatchedTwoEventsOneSubscriptionStatus: two events on one
// subscription render in a single Bundle with one SubscriptionStatus,
// two notificationEvent entries (sorted ascending by eventNumber), and
// for full-resource payloads two focus body entries.
func TestBuildBatchedTwoEventsOneSubscriptionStatus(t *testing.T) {
	t.Parallel()
	sub := repos.SubscriptionRow{
		ID: uuid.New(), TopicURL: "http://example.org/t", Status: repos.SubActive,
		Content: "id-only",
	}
	ev1 := repos.EhrEventRow{
		ID: uuid.New(), EventNumber: 1, TopicURL: "http://example.org/t",
		Focus: "X/1", ChangeKind: repos.ChangeCreate,
		Resource: []byte(`{"resourceType":"X","id":"1"}`), OccurredAt: time.Now(),
	}
	ev2 := repos.EhrEventRow{
		ID: uuid.New(), EventNumber: 2, TopicURL: "http://example.org/t",
		Focus: "X/2", ChangeKind: repos.ChangeUpdate,
		Resource: []byte(`{"resourceType":"X","id":"2"}`), OccurredAt: time.Now().Add(time.Second),
	}
	b := builder.New(builder.Config{})
	env, err := b.Build(context.Background(), builder.Job{
		Subscription:     sub,
		NotificationType: channel.BundleEventNotification,
		Events:           []repos.EhrEventRow{ev1, ev2},
		PerSubEventNumbers: map[uuid.UUID]int64{
			ev1.ID: 7,
			ev2.ID: 8,
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	bundle := unmarshalBundle(t, env.BundleBytes)
	entries := bundle["entry"].([]any)
	if len(entries) != 1 {
		t.Fatalf("id-only batched: want 1 entry (status), got %d", len(entries))
	}
	status := entries[0].(map[string]any)["resource"].(map[string]any)
	notifEvents := status["notificationEvent"].([]any)
	if len(notifEvents) != 2 {
		t.Fatalf("notificationEvent: want 2, got %d", len(notifEvents))
	}
	// Per-subscription event numbers must be in ascending order.
	first := int64(notifEvents[0].(map[string]any)["eventNumber"].(float64))
	second := int64(notifEvents[1].(map[string]any)["eventNumber"].(float64))
	if first != 7 || second != 8 {
		t.Fatalf("eventNumber order: got %d,%d want 7,8", first, second)
	}
	highest := int64(status["eventsSinceSubscriptionStart"].(float64))
	if highest != 8 {
		t.Errorf("eventsSinceSubscriptionStart: want 8 (highest), got %d", highest)
	}
	if env.Sequence != 8 {
		t.Errorf("Envelope.Sequence: want 8, got %d", env.Sequence)
	}
}

// TestBuildSetsBundleTimestampFromClock: Bundle.timestamp is the
// builder's clock. Tests pin the clock to verify.
func TestBuildSetsBundleTimestampFromClock(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	sub := repos.SubscriptionRow{
		ID: uuid.New(), TopicURL: "http://example.org/t", Status: repos.SubActive,
		Content: "id-only",
	}
	b := builder.New(builder.Config{Clock: func() time.Time { return fixed }})
	env, err := b.Build(context.Background(), builder.Job{
		Subscription:     sub,
		NotificationType: channel.BundleHandshake,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	bundle := unmarshalBundle(t, env.BundleBytes)
	got, _ := bundle["timestamp"].(string)
	if got != fixed.Format(time.RFC3339) {
		t.Errorf("Bundle.timestamp: got %q, want %q", got, fixed.Format(time.RFC3339))
	}
}

// TestBuildPropagatesCorrelationID: the envelope's correlation_id is
// the subscription_id-correlated id from the first event (or
// subscription itself for non-event bundles).
func TestBuildPropagatesCorrelationID(t *testing.T) {
	t.Parallel()
	corr := uuid.New()
	sub := repos.SubscriptionRow{
		ID: uuid.New(), TopicURL: "http://example.org/t", Status: repos.SubActive,
		Content: "id-only",
	}
	ev := repos.EhrEventRow{
		ID: uuid.New(), EventNumber: 1, TopicURL: "http://example.org/t",
		Focus: "X/1", ChangeKind: repos.ChangeCreate,
		Resource: []byte(`{"resourceType":"X"}`), CorrelationID: corr,
		OccurredAt: time.Now(),
	}
	b := builder.New(builder.Config{})
	env, err := b.Build(context.Background(), builder.Job{
		Subscription:       sub,
		NotificationType:   channel.BundleEventNotification,
		Events:             []repos.EhrEventRow{ev},
		PerSubEventNumbers: map[uuid.UUID]int64{ev.ID: 1},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if env.CorrelationID != corr.String() {
		t.Errorf("CorrelationID: got %q, want %q", env.CorrelationID, corr.String())
	}
	if env.SubscriptionID != sub.ID {
		t.Errorf("SubscriptionID: got %v want %v", env.SubscriptionID, sub.ID)
	}
}

// TestBuildQueryEventDoesNotAdvanceCursor: query-event bundles are
// structurally identical to event-notification but with type
// "query-event"; the envelope's notification kind reflects this so
// the scheduler knows not to advance the cursor.
func TestBuildQueryEventBundleType(t *testing.T) {
	t.Parallel()
	sub := repos.SubscriptionRow{
		ID: uuid.New(), TopicURL: "http://example.org/t", Status: repos.SubActive,
		Content: "id-only",
	}
	ev := repos.EhrEventRow{
		ID: uuid.New(), EventNumber: 1, TopicURL: "http://example.org/t",
		Focus: "X/1", Resource: []byte(`{"resourceType":"X"}`), OccurredAt: time.Now(),
	}
	b := builder.New(builder.Config{})
	env, err := b.Build(context.Background(), builder.Job{
		Subscription:       sub,
		NotificationType:   channel.BundleQueryEvent,
		Events:             []repos.EhrEventRow{ev},
		PerSubEventNumbers: map[uuid.UUID]int64{ev.ID: 1},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if env.BundleKind != channel.BundleQueryEvent {
		t.Errorf("BundleKind: got %v want query-event", env.BundleKind)
	}
	bundle := unmarshalBundle(t, env.BundleBytes)
	status := bundle["entry"].([]any)[0].(map[string]any)["resource"].(map[string]any)
	if status["type"] != "query-event" {
		t.Errorf("SubscriptionStatus.type: got %v", status["type"])
	}
}

// helper: unmarshal Bundle bytes into a generic map for assertions.
func unmarshalBundle(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("Bundle JSON unmarshal: %v\nbytes: %s", err, b)
	}
	return out
}
