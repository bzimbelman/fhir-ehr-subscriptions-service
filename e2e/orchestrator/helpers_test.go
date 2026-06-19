// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"testing"
	"time"

	hpipe "github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/harness"
)

// Helper contracts pinned by these tests:
//
//   * RegisterSubscriber drives POST /Subscription on the running API
//     and waits for the row to flip to status=active. After story #150
//     the helper does NOT touch the DB: tests assert on the row that
//     the API wrote.
//   * WaitForNotification polls the rest-hook journal until a matching
//     entry shows up, or returns an error on timeout.
//   * AssertResourceChanges queries resource_changes by adapter+correlation
//     and returns the matched rows so scenarios can assert against them.
//
// These are exercise-the-helpers tests; they are not the scenario tests.
// Scenario tests live in *_scenario_test.go.

func TestHelpers_RegisterSubscriber_DrivesAPIAndActivates(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)
	if err := seedHL7Topic(ctx, h.DB); err != nil {
		t.Fatalf("seed topic: %v", err)
	}

	const clientID = "test-client-helper-150"
	api, err := hpipe.StartAPIServer(ctx, hpipe.APIServerConfig{
		Pool:     h.DB,
		ClientID: clientID,
	})
	if err != nil {
		t.Fatalf("api start: %v", err)
	}
	t.Cleanup(func() { _ = api.Close() })

	subID, err := RegisterSubscriber(ctx, h, RegisterSubscriberOptions{
		ClientID:   clientID,
		TopicURL:   "http://example.org/topics/hl7-passthrough",
		Endpoint:   "https://subscriber.example.com/hook/test-client-150",
		APIBaseURL: api.URL,
	})
	if err != nil {
		t.Fatalf("RegisterSubscriber: %v", err)
	}
	if subID == "" {
		t.Fatalf("RegisterSubscriber returned empty id")
	}

	// Read the row back: the API wrote it (we did not).
	var (
		foundClient string
		foundStatus string
	)
	err = h.DB.QueryRow(ctx,
		`select client_id, status from subscriptions where id = $1`, subID).Scan(&foundClient, &foundStatus)
	if err != nil {
		t.Fatalf("scan back: %v", err)
	}
	if foundClient != clientID {
		t.Fatalf("client_id: got %q want %q", foundClient, clientID)
	}
	// The helper's contract is that it returns only after the row is
	// active; assert on that explicitly so a regression to "return on
	// 201" is caught.
	if foundStatus != "active" {
		t.Fatalf("status: got %q want active", foundStatus)
	}
}

func TestHelpers_WaitForNotification_Polls(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Inject a notification asynchronously.
	go func() {
		time.Sleep(100 * time.Millisecond)
		h.MockSub.InjectNotification("sub-helpers-test",
			[]byte(`{"resourceType":"Bundle","id":"helpers-test"}`))
	}()

	got, err := WaitForNotification(ctx, h, "sub-helpers-test", 2*time.Second)
	if err != nil {
		t.Fatalf("WaitForNotification: %v", err)
	}
	if got.SubscriptionID != "sub-helpers-test" {
		t.Fatalf("SubscriptionID: got %q", got.SubscriptionID)
	}
}

func TestHelpers_AssertResourceChanges_FindsRow(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Insert a row by hand to confirm the helper finds it.
	corrID, err := h.InsertResourceChange(ctx, "test-adapter",
		"Patient", "create", []byte(`{"resourceType":"Patient","id":"helper-1"}`))
	if err != nil {
		t.Fatalf("InsertResourceChange: %v", err)
	}

	rows, err := AssertResourceChanges(ctx, h, "test-adapter", corrID)
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
