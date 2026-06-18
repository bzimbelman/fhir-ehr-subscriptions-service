//go:build integration

// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// S-9.9 integration coverage for the per-row retry budget on the
// hl7processor BeginTx failure path. Failures are injected via the
// internal Processor.beginTx delegate so the test can flip between
// transient and persistent failure regimes without rolling the pool.

package hl7processor

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// startPGForRetryBudget spins up a Postgres 16 container or t.Skips if
// Docker is unavailable.
func startPGForRetryBudget(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("hl7p_s99_test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
		tcpostgres.WithSQLDriver("pgx/v5"),
	)
	if err != nil {
		t.Skipf("postgres container unavailable; skipping integration test: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Skipf("connection string unavailable: %v", err)
	}
	return url
}

// startStorageForRetryBudget brings up a *storage.Storage against the
// container.
func startStorageForRetryBudget(t *testing.T, url string) *storage.Storage {
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

// retryBudgetAdapter is a minimal SPI used solely so processOne can
// finish translation when the BeginTx finally succeeds.
type retryBudgetAdapter struct {
	spi.BaseHl7MessageProcessor
}

func (retryBudgetAdapter) Lex(b []byte) (spi.ParsedHL7Message, error) {
	cp := make([]byte, len(b))
	copy(cp, b)
	return spi.ParsedHL7Message{Raw: cp}, nil
}

func (retryBudgetAdapter) Classify(spi.ParsedHL7Message) (spi.Classification, error) {
	return spi.Classification{Kind: spi.ChangeCreate}, nil
}

func (retryBudgetAdapter) MapToFHIR(spi.ParsedHL7Message, spi.Classification) (spi.FhirResource, error) {
	return spi.FhirResource{
		ResourceType: "ServiceRequest",
		Body:         []byte(`{"resourceType":"ServiceRequest"}`),
	}, nil
}

func (retryBudgetAdapter) Validate(spi.FhirResource) error { return nil }

// TestS9_9_PersistentBeginTxFail_DeadLetters_AfterBudget — when BeginTx
// always fails, the row is dead-lettered with reason=tx_begin_failed
// once attempt_count exceeds Config.MaxRowAttempts. Without S-9.9 the
// row stays processed=false forever.
func TestS9_9_PersistentBeginTxFail_DeadLetters_AfterBudget(t *testing.T) {
	t.Parallel()
	url := startPGForRetryBudget(t)
	s := startStorageForRetryBudget(t, url)

	pool := s.Pool().Pgx()
	queue := s.Hl7MessageQueue()

	ctx := context.Background()
	id, err := queue.Insert(ctx, pool, repos.Hl7MessageQueueRow{
		ListenerEndpoint: "test-listener",
		PeerAddr:         "10.0.0.1:5000",
		MllpMessageID:    uuid.New().String(),
		CorrelationID:    uuid.New(),
		RawBody:          []byte("MSH|^~\\&|TEST"),
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	const maxAttempts int32 = 3
	cfg := Config{
		AdapterID:             "default",
		ClaimBatchSize:        16,
		ClaimIdlePollInterval: 50 * time.Millisecond,
		ReaperTickInterval:    1 * time.Second,
		MaxRowAttempts:        maxAttempts,
	}
	p, err := New(cfg, Deps{
		Pool:       pool,
		Codec:      s.Codec(),
		HL7Queue:   queue,
		Pending:    s.PendingPairs(),
		Changes:    s.ResourceChanges(),
		DeadLetter: s.DeadLetters(),
		Adapter:    retryBudgetAdapter{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var beginCalls atomic.Int32
	p.beginTx = func(context.Context, pgx.TxOptions) (pgx.Tx, error) {
		beginCalls.Add(1)
		return nil, errors.New("simulated begin tx failure")
	}

	for i := 0; i < int(maxAttempts)+2; i++ {
		if err := p.claimAndProcessOnce(ctx); err != nil {
			t.Fatalf("claimAndProcessOnce iter %d: %v", i, err)
		}
	}

	if got := beginCalls.Load(); got < maxAttempts+1 {
		t.Errorf("beginTx called %d times; want >= %d", got, maxAttempts+1)
	}

	dls, err := readDeadLettersBySource(ctx, pool, id)
	if err != nil {
		t.Fatalf("readDeadLetters: %v", err)
	}
	if len(dls) != 1 {
		t.Fatalf("expected 1 dead_letters row, got %d", len(dls))
	}
	if !strings.Contains(dls[0].Reason, "tx_begin_failed") {
		t.Errorf("dead_letter reason = %q; want substring tx_begin_failed", dls[0].Reason)
	}

	got, err := queue.GetByID(ctx, pool, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil || !got.Processed {
		t.Fatalf("queue row should be processed=true after dead-letter; got %+v", got)
	}
	if got.AttemptCount < maxAttempts+1 {
		t.Errorf("attempt_count = %d; want >= %d", got.AttemptCount, maxAttempts+1)
	}
}

// TestS9_9_TransientBeginTxFail_RecoversWithoutDeadLetter — a flaky
// BeginTx that succeeds before the budget is exhausted lets the row
// translate normally. attempt_count carries the residue of the failed
// attempts but no dead_letters row is written.
func TestS9_9_TransientBeginTxFail_RecoversWithoutDeadLetter(t *testing.T) {
	t.Parallel()
	url := startPGForRetryBudget(t)
	s := startStorageForRetryBudget(t, url)

	pool := s.Pool().Pgx()
	queue := s.Hl7MessageQueue()

	ctx := context.Background()
	id, err := queue.Insert(ctx, pool, repos.Hl7MessageQueueRow{
		ListenerEndpoint: "test-listener",
		PeerAddr:         "10.0.0.1:5000",
		MllpMessageID:    uuid.New().String(),
		CorrelationID:    uuid.New(),
		RawBody:          []byte("MSH|^~\\&|TEST"),
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	cfg := Config{
		AdapterID:             "default",
		ClaimBatchSize:        16,
		ClaimIdlePollInterval: 50 * time.Millisecond,
		ReaperTickInterval:    1 * time.Second,
		MaxRowAttempts:        8,
	}
	p, err := New(cfg, Deps{
		Pool:       pool,
		Codec:      s.Codec(),
		HL7Queue:   queue,
		Pending:    s.PendingPairs(),
		Changes:    s.ResourceChanges(),
		DeadLetter: s.DeadLetters(),
		Adapter:    retryBudgetAdapter{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Fail the first 2 BeginTx calls, then delegate to the real pool.
	var failsLeft atomic.Int32
	failsLeft.Store(2)
	p.beginTx = func(c context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
		if failsLeft.Add(-1) >= 0 {
			return nil, errors.New("simulated transient begin tx failure")
		}
		return pool.BeginTx(c, opts)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := p.claimAndProcessOnce(ctx); err != nil {
			t.Fatalf("claimAndProcessOnce: %v", err)
		}
		row, err := queue.GetByID(ctx, pool, id)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if row != nil && row.Processed {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	got, err := queue.GetByID(ctx, pool, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("row vanished")
	}
	if !got.Processed {
		t.Fatalf("row should have processed after transient failures cleared; got %+v", got)
	}
	if got.AttemptCount < 2 {
		t.Errorf("attempt_count should reflect 2 failed begins, got %d", got.AttemptCount)
	}

	dls, err := readDeadLettersBySource(ctx, pool, id)
	if err != nil {
		t.Fatalf("readDeadLetters: %v", err)
	}
	if len(dls) != 0 {
		t.Errorf("transient failures must not dead-letter; got %d rows", len(dls))
	}
}

func readDeadLettersBySource(ctx context.Context, q repos.Querier, id uuid.UUID) ([]repos.DeadLetterRow, error) {
	rows, err := q.Query(ctx,
		`SELECT id, kind, source_table, source_id, reason FROM dead_letters WHERE source_id = $1`,
		id,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []repos.DeadLetterRow
	for rows.Next() {
		var r repos.DeadLetterRow
		if err := rows.Scan(&r.ID, &r.Kind, &r.SourceTable, &r.SourceID, &r.Reason); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
