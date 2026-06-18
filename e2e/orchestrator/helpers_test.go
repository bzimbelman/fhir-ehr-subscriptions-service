// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"testing"
	"time"
)

// Helper contracts pinned by these tests:
//
//   * RegisterSubscriber inserts auth_clients + subscriptions rows so a
//     scenario has a known subscriber to fan out to.
//   * WaitForNotification polls the rest-hook journal until a matching
//     entry shows up, or returns an error on timeout.
//   * AssertResourceChanges queries resource_changes by adapter+correlation
//     and returns the matched rows so scenarios can assert against them.
//
// These are exercise-the-helpers tests; they are not the scenario tests.
// Scenario tests live in *_scenario_test.go.

func TestHelpers_RegisterSubscriber_InsertsRows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	subID, err := RegisterSubscriber(ctx, harness, RegisterSubscriberOptions{
		ClientID: "test-client-1",
		TopicURL: "http://example.org/topic/1",
		Endpoint: "http://localhost/hook/test-client-1",
	})
	if err != nil {
		t.Fatalf("RegisterSubscriber: %v", err)
	}
	if subID == "" {
		t.Fatalf("RegisterSubscriber returned empty id")
	}

	// Read it back.
	var foundClient string
	err = harness.DB.QueryRow(ctx,
		`select client_id from subscriptions where id = $1`, subID).Scan(&foundClient)
	if err != nil {
		t.Fatalf("scan back: %v", err)
	}
	if foundClient != "test-client-1" {
		t.Fatalf("client_id: got %q want test-client-1", foundClient)
	}
}

func TestHelpers_WaitForNotification_Polls(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Inject a notification asynchronously.
	go func() {
		time.Sleep(100 * time.Millisecond)
		harness.MockSub.InjectNotification("sub-helpers-test",
			[]byte(`{"resourceType":"Bundle","id":"helpers-test"}`))
	}()

	got, err := WaitForNotification(ctx, harness, "sub-helpers-test", 2*time.Second)
	if err != nil {
		t.Fatalf("WaitForNotification: %v", err)
	}
	if got.SubscriptionID != "sub-helpers-test" {
		t.Fatalf("SubscriptionID: got %q", got.SubscriptionID)
	}
}

func TestHelpers_AssertResourceChanges_FindsRow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Insert a row by hand to confirm the helper finds it.
	corrID, err := harness.InsertResourceChange(ctx, "test-adapter",
		"Patient", "create", []byte(`{"resourceType":"Patient","id":"helper-1"}`))
	if err != nil {
		t.Fatalf("InsertResourceChange: %v", err)
	}

	rows, err := AssertResourceChanges(ctx, harness, "test-adapter", corrID)
	if err != nil {
		t.Fatalf("AssertResourceChanges: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows: got %d want 1", len(rows))
	}
	if rows[0].ResourceType != "Patient" {
		t.Fatalf("ResourceType: got %q", rows[0].ResourceType)
	}
}
