//go:build integration

// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Integration tests for the matcher worker. Requires Docker. Skips
// gracefully if a Postgres container cannot be started — same pattern
// as internal/infra/storage/integration_test.go.
//
// Run with: go test -race -tags integration ./internal/matcher/...

package matcher_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/matcher"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/topics/catalog"
)

// startPostgres spins up a Postgres 16 container or t.Skips when
// Docker is unavailable. Same shape as the storage integration tests,
// with the additional belt-and-braces panic recover because some
// testcontainers code paths (MustExtractDockerHost) panic instead of
// returning a clean error when Docker is not running.
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
			tcpostgres.WithDatabase("matcher_test"),
			tcpostgres.WithUsername("test"),
			tcpostgres.WithPassword("test"),
			tcpostgres.BasicWaitStrategies(),
			tcpostgres.WithSQLDriver("pgx/v5"),
		)
		if err != nil {
			res <- runResult{err: err}
			return
		}
		t.Cleanup(func() {
			_ = container.Terminate(context.Background())
		})
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

const integrationTopic = `{
  "resourceType": "SubscriptionTopic",
  "url": "http://example.org/topics/order-changed",
  "version": "1.0.0",
  "title": "Order changed",
  "status": "active",
  "resourceTrigger": [{
    "resource": "ServiceRequest",
    "supportedInteraction": ["create", "update"],
    "queryCriteria": {
      "current": "status=active"
    }
  }],
  "notificationShape": [{
    "resource": "ServiceRequest",
    "include": ["ServiceRequest:patient"]
  }]
}`

// TestIntegrationMatcherEndToEnd inserts a resource_changes row,
// constructs a catalog with one topic that should match, runs the
// worker once, and asserts that:
//  1. The source row is processed=true.
//  2. An ehr_events row is written with the right topic_url and
//     focus.
func TestIntegrationMatcherEndToEnd(t *testing.T) {
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

	// Register the recipient tenant + an active subscription on the
	// matched topic so the matcher's per-client fan-out (OP #272) has
	// someone to fan out to. Pre-#272 this was an implicit "any topic
	// match emits a row regardless of subscribers"; post-#272 the
	// matcher emits one row per (resource_change × topic ×
	// subscription.client_id).
	if err := s.AuthClients().Insert(ctx, s.Pool().Pgx(), repos.AuthClientRow{
		ID: "client-end-to-end", DisplayName: "client-end-to-end",
	}); err != nil {
		t.Fatalf("auth_clients.Insert: %v", err)
	}
	if _, err := s.Subscriptions().Insert(ctx, s.Pool().Pgx(), repos.SubscriptionRow{
		ClientID:    "client-end-to-end",
		Status:      repos.SubscriptionStatus("active"),
		TopicURL:    "http://example.org/topics/order-changed",
		ChannelType: "rest-hook",
		Endpoint:    "https://example.com/hook",
		Content:     "id-only",
		MaxCount:    1,
	}); err != nil {
		t.Fatalf("subscriptions.Insert: %v", err)
	}

	// Insert a matching resource_changes row.
	plaintext := []byte(`{"resourceType":"ServiceRequest","id":"abc-123","status":"active"}`)
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

	// Drive the worker for exactly one tick.
	worker := matcher.NewWorker(
		s.Pool().Pgx(),
		s.ResourceChanges(),
		s.EhrEvents(),
		func() *catalog.Catalog { return cat },
		matcher.Config{ClaimBatchSize: 1},
	)
	worker.SetSubscriptionsRepo(s.Subscriptions())
	processed, err := worker.TickOnce(ctx)
	if err != nil {
		t.Fatalf("TickOnce: %v", err)
	}
	if !processed {
		t.Fatal("expected the worker to process exactly one row")
	}

	// 1. Source row processed.
	var processedFlag bool
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT processed FROM resource_changes WHERE id=$1`, rcID,
	).Scan(&processedFlag); err != nil {
		t.Fatalf("read processed flag: %v", err)
	}
	if !processedFlag {
		t.Errorf("expected resource_changes.processed=true")
	}

	// 2. Exactly one ehr_events row.
	var n int
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT count(*) FROM ehr_events WHERE resource_change_id=$1`, rcID,
	).Scan(&n); err != nil {
		t.Fatalf("count ehr_events: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 ehr_events row, got %d", n)
	}

	// 3. Row carries the right topic_url and focus.
	var topicURL, focus string
	var corrOut uuid.UUID
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT topic_url, focus, correlation_id
		   FROM ehr_events WHERE resource_change_id=$1`, rcID,
	).Scan(&topicURL, &focus, &corrOut); err != nil {
		t.Fatalf("read ehr_events row: %v", err)
	}
	if topicURL != "http://example.org/topics/order-changed" {
		t.Errorf("topic_url mismatch: %q", topicURL)
	}
	if focus != "ServiceRequest/abc-123" {
		t.Errorf("focus mismatch: %q", focus)
	}
	if corrOut != corr {
		t.Errorf("correlation_id should propagate: got %v want %v", corrOut, corr)
	}

	// 4. Idle: another tick is a no-op.
	processed2, err := worker.TickOnce(ctx)
	if err != nil {
		t.Fatalf("TickOnce idle: %v", err)
	}
	if processed2 {
		t.Errorf("idle tick should report processed=false")
	}
}

// TestIntegrationNoMatchStillFlipsProcessed: a resource_changes row
// that produces zero matches still has processed=true after the tick.
// This exercises the LLD's "no-match case still flips processed" rule.
func TestIntegrationNoMatchStillFlipsProcessed(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	cat, err := emptyCatalog()
	if err != nil {
		t.Fatalf("empty catalog: %v", err)
	}

	rcID, _, err := s.ResourceChanges().Insert(ctx, s.Pool().Pgx(), repos.ResourceChangeRow{
		AdapterID:     "default",
		CorrelationID: uuid.New(),
		ResourceType:  "Observation",
		ChangeKind:    repos.ChangeCreate,
		Resource:      []byte(`{"resourceType":"Observation","status":"final"}`),
		OccurredAt:    time.Now(),
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	worker := matcher.NewWorker(
		s.Pool().Pgx(),
		s.ResourceChanges(),
		s.EhrEvents(),
		func() *catalog.Catalog { return cat },
		matcher.Config{ClaimBatchSize: 1},
	)
	worker.SetSubscriptionsRepo(s.Subscriptions())
	processed, err := worker.TickOnce(ctx)
	if err != nil {
		t.Fatalf("TickOnce: %v", err)
	}
	if !processed {
		t.Fatal("expected the worker to process the row even with zero matches")
	}

	var processedFlag bool
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT processed FROM resource_changes WHERE id=$1`, rcID,
	).Scan(&processedFlag); err != nil {
		t.Fatalf("read processed: %v", err)
	}
	if !processedFlag {
		t.Errorf("expected processed=true on no-match row")
	}

	var n int
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT count(*) FROM ehr_events WHERE resource_change_id=$1`, rcID,
	).Scan(&n); err != nil {
		t.Fatalf("count ehr_events: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 ehr_events rows on no-match, got %d", n)
	}
}

func emptyCatalog() (*catalog.Catalog, error) {
	rep, err := catalog.Load(catalog.Sources{})
	if err != nil {
		return nil, err
	}
	return rep.Catalog, nil
}
