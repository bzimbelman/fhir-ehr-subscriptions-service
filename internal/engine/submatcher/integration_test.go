//go:build integration

// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Integration tests for the submatcher worker. Requires Docker. Skips
// gracefully if Docker is unavailable — same shape as the matcher's
// integration tests.
//
// Run: go test -race -tags integration ./internal/engine/submatcher/...

package submatcher_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/engine/submatcher"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/storage"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/storage/repos"
)

func startPostgres(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	type runResult struct {
		url string
		err error
	}
	res := make(chan runResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				res <- runResult{err: fmt.Errorf("docker not available: %v", r)}
			}
		}()
		container, err := tcpostgres.Run(ctx,
			"postgres:16-alpine",
			tcpostgres.WithDatabase("submatcher_test"),
			tcpostgres.WithUsername("test"),
			tcpostgres.WithPassword("test"),
			tcpostgres.BasicWaitStrategies(),
			tcpostgres.WithSQLDriver("pgx/v5"),
		)
		if err != nil {
			res <- runResult{err: err}
			return
		}
		t.Cleanup(func() { _ = container.Terminate(context.Background()) })
		url, err := container.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			res <- runResult{err: err}
			return
		}
		res <- runResult{url: url}
	}()
	r := <-res
	if r.err != nil {
		t.Skipf("postgres container unavailable; skipping integration test: %v", r.err)
	}
	return r.url
}

func newTestStorage(t *testing.T, url string) *storage.Storage {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	cfg := storage.Config{
		PostgresURL: url,
		KeyVersions: map[int32][]byte{1: key},
		ActiveKey:   1,
	}
	cfg.Partitioning.AutoDrop = false
	cfg.Partitioning.RunInterval = time.Hour
	cfg.Retention.RunInterval = time.Hour
	cfg.Retention.Hl7MessageQueue = 0

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, err := storage.Start(ctx, cfg, storage.Context{})
	if err != nil {
		t.Fatalf("storage.Start: %v", err)
	}
	t.Cleanup(func() {
		shctx, sc := context.WithTimeout(context.Background(), 10*time.Second)
		defer sc()
		_ = s.Shutdown(shctx)
	})
	return s
}

// seedAuthClient inserts an auth_clients row required by the
// subscriptions FK; the engine never reads it but the schema does.
func seedAuthClient(t *testing.T, s *storage.Storage, id string) {
	t.Helper()
	if _, err := s.Pool().Pgx().Exec(context.Background(),
		`INSERT INTO auth_clients(id) VALUES ($1) ON CONFLICT DO NOTHING`, id,
	); err != nil {
		t.Fatalf("seed auth_client: %v", err)
	}
}

// seedEhrEvent inserts an ehr_events row directly (bypassing topic
// matcher) so the test owns the input shape. Returns the row id +
// event_number.
func seedEhrEvent(
	t *testing.T,
	s *storage.Storage,
	topicURL, focus string,
	resource []byte,
) (uuid.UUID, int64) {
	t.Helper()
	id, evNum, err := s.EhrEvents().Insert(context.Background(), s.Pool().Pgx(), repos.EhrEventRow{
		TopicURL:         topicURL,
		Focus:            focus,
		ChangeKind:       repos.ChangeCreate,
		Resource:         resource,
		CorrelationID:    uuid.New(),
		OccurredAt:       time.Now().UTC(),
		ResourceChangeID: uuid.New(),
	})
	if err != nil {
		t.Fatalf("seed ehr_events: %v", err)
	}
	return id, evNum
}

func filterByJSON(clauses ...map[string]string) []byte {
	out := make([]map[string]string, 0, len(clauses))
	out = append(out, clauses...)
	b, _ := json.Marshal(out)
	return b
}

// TestIntegrationSubmatcherOneMatchOneDelivery: an ehr_events row plus
// one active subscription whose filterBy matches → exactly one
// deliveries row, source row processed.
func TestIntegrationSubmatcherOneMatchOneDelivery(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	seedAuthClient(t, s, "client-A")
	subID, err := s.Subscriptions().Insert(ctx, s.Pool().Pgx(), repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/order-changed",
		ChannelType: "rest-hook",
		Endpoint:    "https://sub.example.org/notif",
		FilterBy:    filterByJSON(map[string]string{"filterParameter": "patient", "value": "Patient/123"}),
		Content:     "id-only",
	})
	if err != nil {
		t.Fatalf("insert sub: %v", err)
	}

	rcID, evNum := seedEhrEvent(t, s, "http://example.org/order-changed", "ServiceRequest/abc",
		[]byte(`{"resourceType":"ServiceRequest","id":"abc","subject":{"reference":"Patient/123"},"status":"active"}`),
	)

	w := submatcher.NewWorker(s.Pool().Pgx(), s.Subscriptions(), s.EhrEvents(), s.Deliveries(), submatcher.Config{ClaimBatchSize: 1})
	processed, err := w.TickOnce(ctx)
	if err != nil {
		t.Fatalf("TickOnce: %v", err)
	}
	if !processed {
		t.Fatal("expected processed=true (one row pending)")
	}

	// Source row processed.
	var processedFlag bool
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT processed FROM ehr_events WHERE id=$1`, rcID,
	).Scan(&processedFlag); err != nil {
		t.Fatalf("read processed: %v", err)
	}
	if !processedFlag {
		t.Errorf("ehr_events.processed should be true")
	}

	// Exactly one deliveries row.
	var n int
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT count(*) FROM deliveries WHERE ehr_event_id=$1 AND subscription_id=$2`, rcID, subID,
	).Scan(&n); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 delivery, got %d", n)
	}

	// Per-subscription event_number starts at 1.
	var perSubEv int64
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT event_number FROM deliveries WHERE subscription_id=$1`, subID,
	).Scan(&perSubEv); err != nil {
		t.Fatalf("read event_number: %v", err)
	}
	if perSubEv != 1 {
		t.Fatalf("want per-sub event_number=1, got %d", perSubEv)
	}

	// Subscription cursor advanced.
	subRow, err := s.Subscriptions().GetByID(ctx, s.Pool().Pgx(), subID)
	if err != nil || subRow == nil {
		t.Fatalf("get sub: %v", err)
	}
	if subRow.EventsSinceSubscriptionStart != 1 {
		t.Fatalf("cursor: want 1, got %d", subRow.EventsSinceSubscriptionStart)
	}
	_ = evNum
}

// TestIntegrationSubmatcherTwoMatchingSubsTwoDeliveries: one event,
// two subscriptions with the same topic + matching filterBy → two
// deliveries rows, both keyed (subscription_id, event_number=1).
func TestIntegrationSubmatcherTwoMatchingSubsTwoDeliveries(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	seedAuthClient(t, s, "client-A")
	seedAuthClient(t, s, "client-B")
	subA, _ := s.Subscriptions().Insert(ctx, s.Pool().Pgx(), repos.SubscriptionRow{
		ClientID: "client-A", Status: repos.SubActive,
		TopicURL: "http://example.org/t", ChannelType: "rest-hook",
		Endpoint: "https://a/", Content: "id-only",
	})
	subB, _ := s.Subscriptions().Insert(ctx, s.Pool().Pgx(), repos.SubscriptionRow{
		ClientID: "client-B", Status: repos.SubActive,
		TopicURL: "http://example.org/t", ChannelType: "rest-hook",
		Endpoint: "https://b/", Content: "id-only",
		FilterBy: filterByJSON(map[string]string{"filterParameter": "status", "value": "active"}),
	})

	rcID, _ := seedEhrEvent(t, s, "http://example.org/t", "X/1",
		[]byte(`{"resourceType":"X","id":"1","status":"active"}`))

	w := submatcher.NewWorker(s.Pool().Pgx(), s.Subscriptions(), s.EhrEvents(), s.Deliveries(), submatcher.Config{ClaimBatchSize: 1})
	if _, err := w.TickOnce(ctx); err != nil {
		t.Fatalf("TickOnce: %v", err)
	}

	var n int
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT count(*) FROM deliveries WHERE ehr_event_id=$1`, rcID,
	).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("want 2 deliveries, got %d", n)
	}

	for _, id := range []uuid.UUID{subA, subB} {
		var ev int64
		if err := s.Pool().Pgx().QueryRow(ctx,
			`SELECT event_number FROM deliveries WHERE subscription_id=$1`, id,
		).Scan(&ev); err != nil {
			t.Fatalf("read: %v", err)
		}
		if ev != 1 {
			t.Fatalf("sub %s: want event_number=1, got %d", id, ev)
		}
	}
}

// TestIntegrationSubmatcherFilterExcludes: one event, one subscription
// whose filterBy excludes it → zero deliveries rows, source row
// processed.
func TestIntegrationSubmatcherFilterExcludes(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	seedAuthClient(t, s, "client-A")
	if _, err := s.Subscriptions().Insert(ctx, s.Pool().Pgx(), repos.SubscriptionRow{
		ClientID: "client-A", Status: repos.SubActive,
		TopicURL: "http://example.org/t", ChannelType: "rest-hook",
		Endpoint: "https://a/", Content: "id-only",
		FilterBy: filterByJSON(map[string]string{"filterParameter": "patient", "value": "Patient/999"}),
	}); err != nil {
		t.Fatalf("seed sub: %v", err)
	}

	rcID, _ := seedEhrEvent(t, s, "http://example.org/t", "X/1",
		[]byte(`{"resourceType":"X","id":"1","subject":{"reference":"Patient/100"}}`))

	w := submatcher.NewWorker(s.Pool().Pgx(), s.Subscriptions(), s.EhrEvents(), s.Deliveries(), submatcher.Config{ClaimBatchSize: 1})
	processed, err := w.TickOnce(ctx)
	if err != nil {
		t.Fatalf("TickOnce: %v", err)
	}
	if !processed {
		t.Fatal("expected processed=true even with no matches")
	}

	var n int
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT count(*) FROM deliveries WHERE ehr_event_id=$1`, rcID,
	).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("want 0 deliveries, got %d", n)
	}

	var processedFlag bool
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT processed FROM ehr_events WHERE id=$1`, rcID,
	).Scan(&processedFlag); err != nil {
		t.Fatalf("read processed: %v", err)
	}
	if !processedFlag {
		t.Errorf("ehr_events.processed should be true on no-match")
	}
}

// TestIntegrationSubmatcherTwoEventsPerSubAdvancesCursor: two events
// for the same subscription produce per-sub event_number 1 and 2 and
// the cursor advances to 2.
func TestIntegrationSubmatcherTwoEventsPerSubAdvancesCursor(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	seedAuthClient(t, s, "client-A")
	subID, _ := s.Subscriptions().Insert(ctx, s.Pool().Pgx(), repos.SubscriptionRow{
		ClientID: "client-A", Status: repos.SubActive,
		TopicURL: "http://example.org/t", ChannelType: "rest-hook",
		Endpoint: "https://a/", Content: "id-only",
	})

	for i := 0; i < 2; i++ {
		seedEhrEvent(t, s, "http://example.org/t", fmt.Sprintf("X/%d", i),
			[]byte(`{"resourceType":"X","id":"x","status":"active"}`))
	}

	w := submatcher.NewWorker(s.Pool().Pgx(), s.Subscriptions(), s.EhrEvents(), s.Deliveries(), submatcher.Config{ClaimBatchSize: 1})
	for i := 0; i < 2; i++ {
		if _, err := w.TickOnce(ctx); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}

	rows, err := s.Pool().Pgx().Query(ctx,
		`SELECT event_number FROM deliveries WHERE subscription_id=$1 ORDER BY event_number`, subID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
	}
	want := []int64{1, 2}
	if len(got) != len(want) {
		t.Fatalf("want %v event_numbers, got %v", want, got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("event_number[%d]: want %d, got %d", i, want[i], got[i])
		}
	}

	subRow, err := s.Subscriptions().GetByID(ctx, s.Pool().Pgx(), subID)
	if err != nil || subRow == nil {
		t.Fatalf("get sub: %v", err)
	}
	if subRow.EventsSinceSubscriptionStart != 2 {
		t.Fatalf("cursor: want 2, got %d", subRow.EventsSinceSubscriptionStart)
	}
}
