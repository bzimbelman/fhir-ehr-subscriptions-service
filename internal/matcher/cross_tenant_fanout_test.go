//go:build integration

// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Cross-tenant fan-out integration tests for OP #272.
//
// Background: pre-#272 the matcher emitted one ehr_events row per
// (resource_change × topic), with no client_id. That made $events
// reads per-tenant impossible (OP #197) because two tenants subscribed
// to the same topic shared an event log.
//
// The fan-out rule is: for each Match the matcher produces, look up the
// distinct client_ids of all active subscriptions for the matched topic
// and emit one ehr_events row per client_id. Each row carries the
// recipient's client_id so the $events handler can filter to "events
// for this caller's client_id only".
//
// Drives a real Postgres container via testcontainers — NO mocks. Tests
// fail closed if Docker is unavailable.
//
// Run with: go test -race -tags integration ./internal/matcher/...

package matcher_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/matcher"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/topics/catalog"
)

// TestIntegrationFanOutPerClientID asserts that when two distinct
// clients (A and B) both have active subscriptions on the same topic
// and a single resource_change matches that topic, the matcher writes
// exactly two ehr_events rows — one per client_id — and each row
// carries the recipient client's id.
//
// This is the OP #272 acceptance criterion.
func TestIntegrationFanOutPerClientID(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	rep, err := catalog.Load(catalog.Sources{
		BuiltIn: []catalog.RawTopic{
			{Origin: "builtin/order-changed", Bytes: []byte(integrationTopic)},
		},
	})
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	if len(rep.Rejected) != 0 {
		t.Fatalf("rejections: %#v", rep.Rejected)
	}
	cat := rep.Catalog
	topicURL := "http://example.org/topics/order-changed"

	// Register two distinct tenants A and B and an active subscription
	// for each on the SAME topic.
	mustInsertAuthClient(t, ctx, s, "client-a")
	mustInsertAuthClient(t, ctx, s, "client-b")
	subA := mustInsertActiveSub(t, ctx, s, "client-a", topicURL)
	subB := mustInsertActiveSub(t, ctx, s, "client-b", topicURL)

	// Drive one matching resource_changes row through the worker.
	plaintext := []byte(`{"resourceType":"ServiceRequest","id":"sr-1","status":"active"}`)
	corr := uuid.New()
	rcID, _, err := s.ResourceChanges().Insert(ctx, s.Pool().Pgx(), repos.ResourceChangeRow{
		AdapterID:     "default",
		CorrelationID: corr,
		ResourceType:  "ServiceRequest",
		ChangeKind:    repos.ChangeCreate,
		Resource:      plaintext,
		OccurredAt:    time.Now(),
	})
	if err != nil {
		t.Fatalf("insert resource_changes: %v", err)
	}

	worker := matcher.NewWorker(
		s.Pool().Pgx(),
		s.ResourceChanges(),
		s.EhrEvents(),
		func() *catalog.Catalog { return cat },
		matcher.Config{ClaimBatchSize: 1},
	)
	// The worker enumerates subscriptions per (topic) and fans
	// ehr_events out per distinct client_id (B-272). The SetSubscriptionsRepo
	// seam injects the real repo so the worker can issue
	// `select distinct client_id from subscriptions where topic_url=$1
	// and status='active'` inside the same tx.
	worker.SetSubscriptionsRepo(s.Subscriptions())

	processed, err := worker.TickOnce(ctx)
	if err != nil {
		t.Fatalf("TickOnce: %v", err)
	}
	if !processed {
		t.Fatal("expected the worker to process exactly one row")
	}

	// Assert two ehr_events rows, one per client_id, both pointing at
	// the same source resource_change.
	type ehrRow struct {
		clientID string
		topicURL string
	}
	var rows []ehrRow
	q, err := s.Pool().Pgx().Query(ctx,
		`SELECT client_id, topic_url FROM ehr_events WHERE resource_change_id=$1 ORDER BY client_id`, rcID)
	if err != nil {
		t.Fatalf("query ehr_events: %v", err)
	}
	defer q.Close()
	for q.Next() {
		var r ehrRow
		if err := q.Scan(&r.clientID, &r.topicURL); err != nil {
			t.Fatalf("scan: %v", err)
		}
		rows = append(rows, r)
	}
	if err := q.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 ehr_events rows (one per client), got %d: %#v", len(rows), rows)
	}
	got := []string{rows[0].clientID, rows[1].clientID}
	sort.Strings(got)
	want := []string{"client-a", "client-b"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("client_id set: got=%v want=%v", got, want)
		}
	}
	for _, r := range rows {
		if r.topicURL != topicURL {
			t.Errorf("topic_url mismatch on row %+v", r)
		}
	}

	// And no ehr_events row was written with a NULL client_id (the
	// pre-#272 shape). The migration must enforce NOT NULL.
	var nullCount int
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT count(*) FROM ehr_events WHERE client_id IS NULL`).Scan(&nullCount); err != nil {
		t.Fatalf("count null client_id: %v", err)
	}
	if nullCount != 0 {
		t.Fatalf("expected 0 ehr_events rows with NULL client_id, got %d", nullCount)
	}

	_ = subA
	_ = subB
}

// TestIntegrationFanOutEventsFilterIsolation asserts the OP #274 rule:
// after a matched resource_change has been fanned out per-client,
// PgEventsStore.ListByTopicAndRangePage filtered by client_id returns
// ONLY that client's rows. No cross-tenant leakage.
func TestIntegrationFanOutEventsFilterIsolation(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	rep, err := catalog.Load(catalog.Sources{
		BuiltIn: []catalog.RawTopic{
			{Origin: "builtin/order-changed", Bytes: []byte(integrationTopic)},
		},
	})
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	cat := rep.Catalog
	topicURL := "http://example.org/topics/order-changed"

	mustInsertAuthClient(t, ctx, s, "tenant-a")
	mustInsertAuthClient(t, ctx, s, "tenant-b")
	mustInsertActiveSub(t, ctx, s, "tenant-a", topicURL)
	mustInsertActiveSub(t, ctx, s, "tenant-b", topicURL)

	rcID, _, err := s.ResourceChanges().Insert(ctx, s.Pool().Pgx(), repos.ResourceChangeRow{
		AdapterID:     "default",
		CorrelationID: uuid.New(),
		ResourceType:  "ServiceRequest",
		ChangeKind:    repos.ChangeCreate,
		Resource:      []byte(`{"resourceType":"ServiceRequest","id":"x","status":"active"}`),
		OccurredAt:    time.Now(),
	})
	if err != nil {
		t.Fatalf("insert resource_changes: %v", err)
	}

	worker := matcher.NewWorker(
		s.Pool().Pgx(),
		s.ResourceChanges(),
		s.EhrEvents(),
		func() *catalog.Catalog { return cat },
		matcher.Config{ClaimBatchSize: 1},
	)
	worker.SetSubscriptionsRepo(s.Subscriptions())
	if _, err := worker.TickOnce(ctx); err != nil {
		t.Fatalf("TickOnce: %v", err)
	}
	_ = rcID

	// Drive a per-client filtered read directly against ehr_events
	// using the same predicate the handler will use. The handler-level
	// signature change is the pg_stores.go ListByTopicAndRangePage
	// taking a client_id parameter (OP #274).
	rowsForA := mustQueryEventsForClient(t, ctx, s, topicURL, "tenant-a")
	rowsForB := mustQueryEventsForClient(t, ctx, s, topicURL, "tenant-b")
	rowsForGhost := mustQueryEventsForClient(t, ctx, s, topicURL, "tenant-ghost")

	if len(rowsForA) != 1 {
		t.Errorf("tenant-a: want 1 event, got %d", len(rowsForA))
	}
	if len(rowsForB) != 1 {
		t.Errorf("tenant-b: want 1 event, got %d", len(rowsForB))
	}
	if len(rowsForGhost) != 0 {
		t.Errorf("tenant-ghost (no subscription): want 0 events, got %d", len(rowsForGhost))
	}
	// Cross-leak check: A's row id MUST NOT appear in B's set.
	if len(rowsForA) > 0 && len(rowsForB) > 0 && rowsForA[0] == rowsForB[0] {
		t.Errorf("cross-tenant leakage: A and B both saw event %v", rowsForA[0])
	}
}

// mustInsertAuthClient inserts an auth_clients row or fails the test.
func mustInsertAuthClient(t *testing.T, ctx context.Context, s *storage.Storage, id string) {
	t.Helper()
	if err := s.AuthClients().Insert(ctx, s.Pool().Pgx(), repos.AuthClientRow{
		ID:          id,
		DisplayName: id,
		Scopes:      []string{"system/Subscription.cruds"},
	}); err != nil {
		t.Fatalf("auth_clients.Insert(%s): %v", id, err)
	}
}

// mustInsertActiveSub inserts an active subscription bound to clientID
// for topicURL and returns the new subscription id.
func mustInsertActiveSub(t *testing.T, ctx context.Context, s *storage.Storage, clientID, topicURL string) uuid.UUID {
	t.Helper()
	id, err := s.Subscriptions().Insert(ctx, s.Pool().Pgx(), repos.SubscriptionRow{
		ClientID:    clientID,
		Status:      repos.SubscriptionStatus("active"),
		TopicURL:    topicURL,
		ChannelType: "rest-hook",
		Endpoint:    "https://example.com/hook",
		Content:     "id-only",
		MaxCount:    1,
	})
	if err != nil {
		t.Fatalf("subscriptions.Insert(%s): %v", clientID, err)
	}
	return id
}

// mustQueryEventsForClient returns event ids for a (topic_url, client_id)
// pair. Mirrors the post-#274 ListByTopicAndRangePage signature so the
// test pins the exact query the handler will issue.
func mustQueryEventsForClient(t *testing.T, ctx context.Context, s *storage.Storage, topicURL, clientID string) []uuid.UUID {
	t.Helper()
	const sql = `
		SELECT id FROM ehr_events
		WHERE topic_url = $1 AND client_id = $2
		ORDER BY event_number ASC`
	q, err := s.Pool().Pgx().Query(ctx, sql, topicURL, clientID)
	if err != nil {
		t.Fatalf("query ehr_events for %s: %v", clientID, err)
	}
	defer q.Close()
	var out []uuid.UUID
	for q.Next() {
		var id uuid.UUID
		if err := q.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, id)
	}
	if err := q.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	return out
}
