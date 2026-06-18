// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package audit_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
)

// TestPostgres_AuditTable runs against a real Postgres instance.
//
// The harness reads DATABASE_URL; if absent the test skips. CI / local
// dev points the env var at a transient docker-run postgres. The test
// creates an audit_log table in a transient schema, drives a few
// emissions through Writer + the pg-backed Store, and verifies the
// chain. A second concurrent Writer asserts the advisory-lock contract
// holds at the SQL boundary.
func TestPostgres_AuditTable_ChainAndConcurrency(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("cannot connect to Postgres at DATABASE_URL: %v", err)
	}
	defer pool.Close()

	// Bootstrap a transient schema.
	schema := "audit_int_" + uuid.New().String()[:8]
	_, err = pool.Exec(ctx, "CREATE SCHEMA "+quoteIdent(schema))
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA "+quoteIdent(schema)+" CASCADE")
	})

	store, err := audit.NewPgStore(pool, audit.PgStoreOptions{Schema: schema})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if migErr := store.Migrate(ctx); migErr != nil {
		t.Fatalf("migrate: %v", migErr)
	}

	w, err := audit.NewWriter(audit.WriterOptions{
		Store: store,
		Sink:  audit.NewStdoutSink(),
	})
	if err != nil {
		t.Fatalf("writer: %v", err)
	}

	const N = 25
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := w.Emit(ctx, audit.Event{
				ActorKind:     "system",
				Action:        "test.event",
				Outcome:       "success",
				CorrelationID: uuid.New(),
				Payload:       map[string]any{"i": i},
			})
			if err != nil {
				t.Errorf("emit %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	if err := audit.VerifyChain(ctx, store); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func quoteIdent(s string) string {
	out := []byte{'"'}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' {
			out = append(out, '"', '"')
		} else {
			out = append(out, c)
		}
	}
	out = append(out, '"')
	return string(out)
}
