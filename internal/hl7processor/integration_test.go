//go:build integration

// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Integration tests for the HL7 Message Processor. Requires Docker.
// Skips gracefully (via t.Skip) if a Postgres container cannot be started.
//
// Run with: go test -race -tags integration ./internal/hl7processor/...

package hl7processor_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/adapter/spi"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/hl7processor"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/storage"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/storage/repos"
)

// startPostgres returns a connection URL for a Postgres 16 container
// or t.Skip if Docker isn't available.
func startPostgres(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("hl7p_test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
		tcpostgres.WithSQLDriver("pgx/v5"),
	)
	if err != nil {
		t.Skipf("postgres container unavailable; skipping integration test: %v", err)
	}
	t.Cleanup(func() {
		_ = container.Terminate(context.Background())
	})

	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Skipf("connection string unavailable: %v", err)
	}
	return url
}

func newStorage(t *testing.T, url string) *storage.Storage {
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

// scriptedAdapter is an SPI implementation tests configure per scenario.
type scriptedAdapter struct {
	spi.BaseHl7MessageProcessor
	classify func(spi.ParsedHL7Message) (spi.Classification, error)
	mapFn    func(spi.ParsedHL7Message, spi.Classification) (spi.FhirResource, error)
	validate func(spi.FhirResource) error
}

func (s *scriptedAdapter) Lex(b []byte) (spi.ParsedHL7Message, error) {
	cp := make([]byte, len(b))
	copy(cp, b)
	return spi.ParsedHL7Message{Raw: cp}, nil
}

func (s *scriptedAdapter) Classify(p spi.ParsedHL7Message) (spi.Classification, error) {
	if s.classify != nil {
		return s.classify(p)
	}
	return spi.Classification{Kind: spi.ChangeCreate}, nil
}

func (s *scriptedAdapter) MapToFHIR(p spi.ParsedHL7Message, c spi.Classification) (spi.FhirResource, error) {
	if s.mapFn != nil {
		return s.mapFn(p, c)
	}
	return spi.FhirResource{
		ResourceType: "ServiceRequest",
		Body:         []byte(`{"resourceType":"ServiceRequest"}`),
	}, nil
}

func (s *scriptedAdapter) Validate(r spi.FhirResource) error {
	if s.validate != nil {
		return s.validate(r)
	}
	return nil
}

// newProcessor builds a Processor wired against the live storage.
func newProcessor(t *testing.T, s *storage.Storage, ad spi.Hl7MessageProcessor, holdWindow time.Duration) *hl7processor.Processor {
	t.Helper()
	cfg := hl7processor.Config{
		AdapterID:             "default",
		ClaimBatchSize:        16,
		ClaimIdlePollInterval: 50 * time.Millisecond,
		ReaperTickInterval:    50 * time.Millisecond,
		CorrelationHoldWindow: holdWindow,
	}
	p, err := hl7processor.New(cfg, hl7processor.Deps{
		Pool:       s.Pool().Pgx(),
		Codec:      s.Codec(),
		HL7Queue:   s.Hl7MessageQueue(),
		Pending:    s.PendingPairs(),
		Changes:    s.ResourceChanges(),
		DeadLetter: s.DeadLetters(),
		Adapter:    ad,
	})
	if err != nil {
		t.Fatalf("hl7processor.New: %v", err)
	}
	return p
}

// insertQueueRow seeds one hl7_message_queue row.
func insertQueueRow(t *testing.T, s *storage.Storage, listener string, body []byte) (uuid.UUID, uuid.UUID) {
	t.Helper()
	corr := uuid.New()
	id, err := s.Hl7MessageQueue().Insert(context.Background(), s.Pool().Pgx(), repos.Hl7MessageQueueRow{
		ListenerEndpoint: listener,
		PeerAddr:         "10.0.0.1:5000",
		MllpMessageID:    uuid.New().String(),
		CorrelationID:    corr,
		RawBody:          body,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	return id, corr
}

// waitFor polls fn up to timeout. Returns nil when fn returns true.
func waitFor(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("waitFor: condition not met within %v", timeout)
}

func runProcessor(t *testing.T, p *hl7processor.Processor) (cancel context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	go func() {
		_ = p.Run(ctx)
		close(doneCh)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-doneCh:
		case <-time.After(5 * time.Second):
			t.Error("processor did not stop after cancel")
		}
	})
	return cancel, doneCh
}

// TestIntegration_SingleMessage_Success: one queued message, no pairing,
// produces one resource_changes row and marks the source processed.
func TestIntegration_SingleMessage_Success(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newStorage(t, url)

	ad := &scriptedAdapter{}
	p := newProcessor(t, s, ad, 30*time.Second)
	runProcessor(t, p)

	id, corr := insertQueueRow(t, s, "adt-feed", []byte("MSH|test"))

	waitFor(t, 5*time.Second, func() bool { return isProcessed(t, s, id) })

	rc := countResourceChanges(t, s, corr)
	if rc != 1 {
		t.Fatalf("expected 1 resource_changes row, got %d", rc)
	}
	if dl := countDeadLetters(t, s); dl != 0 {
		t.Errorf("unexpected dead_letters rows: %d", dl)
	}
	if pp := countPendingPairs(t, s); pp != 0 {
		t.Errorf("unexpected pending_pairs rows: %d", pp)
	}
}

// TestIntegration_ValidationFail_DeadLetters: a validation failure marks
// the source row processed and writes one dead_letters row, with no
// resource_changes row. (LLD §9: validation -> hl7_invalid_fhir.)
func TestIntegration_ValidationFail_DeadLetters(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newStorage(t, url)

	ad := &scriptedAdapter{
		validate: func(spi.FhirResource) error { return errors.New("missing required") },
	}
	p := newProcessor(t, s, ad, 30*time.Second)
	runProcessor(t, p)

	id, corr := insertQueueRow(t, s, "adt-feed", []byte("MSH|broken"))

	waitFor(t, 5*time.Second, func() bool { return isProcessed(t, s, id) })

	if rc := countResourceChanges(t, s, corr); rc != 0 {
		t.Errorf("validation fail must not write resource_changes; got %d", rc)
	}
	dl := readDeadLetters(t, s, id)
	if len(dl) != 1 {
		t.Fatalf("expected 1 dead_letter row, got %d", len(dl))
	}
	if dl[0].Kind != "hl7_invalid_fhir" {
		t.Errorf("kind: got %q want hl7_invalid_fhir", dl[0].Kind)
	}
	if dl[0].SourceTable != "hl7_message_queue" {
		t.Errorf("source_table: %q", dl[0].SourceTable)
	}
}

// TestIntegration_CancelReplacePair_WithinWindow: a cancel + replace
// arriving in order under the same correlation key produces ONE
// resource_changes update row. Both source rows are marked processed
// and the pending row is gone.
func TestIntegration_CancelReplacePair_WithinWindow(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newStorage(t, url)

	const corrKey = "ORC-2:placeholder-1234"
	ad := &scriptedAdapter{
		classify: func(p spi.ParsedHL7Message) (spi.Classification, error) {
			switch string(p.Raw) {
			case "CANCEL":
				return spi.Classification{Kind: spi.ChangeDelete, CorrelationKey: corrKey}, nil
			case "REPLACE":
				return spi.Classification{Kind: spi.ChangeCreate, CorrelationKey: corrKey}, nil
			}
			return spi.Classification{Kind: spi.ChangeCreate}, nil
		},
		mapFn: func(p spi.ParsedHL7Message, _ spi.Classification) (spi.FhirResource, error) {
			id := "old"
			body := []byte(`{"resourceType":"ServiceRequest","id":"old","status":"revoked"}`)
			if string(p.Raw) == "REPLACE" {
				id = "new"
				body = []byte(`{"resourceType":"ServiceRequest","id":"new","status":"active"}`)
			}
			return spi.FhirResource{ResourceType: "ServiceRequest", ID: id, Body: body}, nil
		},
	}
	p := newProcessor(t, s, ad, 30*time.Second)
	runProcessor(t, p)

	cancelID, cancelCorr := insertQueueRow(t, s, "orders", []byte("CANCEL"))
	// Wait for the cancellation to be held in pending_pairs.
	waitFor(t, 5*time.Second, func() bool {
		return countPendingPairs(t, s) == 1
	})
	// Source row must remain unprocessed while the half is held.
	if isProcessed(t, s, cancelID) {
		t.Fatal("cancellation source must remain unprocessed while held")
	}

	replaceID, _ := insertQueueRow(t, s, "orders", []byte("REPLACE"))
	// Wait for resolution: pending row gone + both sources processed.
	waitFor(t, 5*time.Second, func() bool {
		if countPendingPairs(t, s) != 0 {
			return false
		}
		return isProcessed(t, s, cancelID) && isProcessed(t, s, replaceID)
	})

	if rc := countResourceChanges(t, s, cancelCorr); rc != 1 {
		t.Fatalf("expected 1 merged update under held correlation_id, got %d", rc)
	}
	row1 := readResourceChange(t, s, cancelCorr)
	if row1.ChangeKind != repos.ChangeUpdate {
		t.Fatalf("change_kind: %s want update", row1.ChangeKind)
	}
	if len(row1.PreviousResource) == 0 {
		t.Fatal("previous_resource should be populated for cancel+replace")
	}
}

// TestIntegration_CancelReplaceExpired_FlushesPlainDelete: a lone
// cancellation whose hold window expires produces a plain delete via
// the SPI's OnUnpairedCancellation callback.
func TestIntegration_CancelReplaceExpired_FlushesPlainDelete(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newStorage(t, url)

	const corrKey = "lone-cancel-1"
	ad := &scriptedAdapter{
		classify: func(spi.ParsedHL7Message) (spi.Classification, error) {
			return spi.Classification{Kind: spi.ChangeDelete, CorrelationKey: corrKey}, nil
		},
		mapFn: func(spi.ParsedHL7Message, spi.Classification) (spi.FhirResource, error) {
			return spi.FhirResource{
				ResourceType: "ServiceRequest",
				ID:           "lone",
				Body:         []byte(`{"resourceType":"ServiceRequest","id":"lone","status":"revoked"}`),
			}, nil
		},
	}
	// 100ms hold window so the test does not block.
	p := newProcessor(t, s, ad, 100*time.Millisecond)
	runProcessor(t, p)

	id, corr := insertQueueRow(t, s, "orders", []byte("CANCEL"))

	// First it should be held.
	waitFor(t, 5*time.Second, func() bool {
		return countPendingPairs(t, s) == 1
	})

	// After hold window + reaper tick, the pending row should be gone
	// and a plain delete written to resource_changes.
	waitFor(t, 5*time.Second, func() bool {
		if countPendingPairs(t, s) != 0 {
			return false
		}
		return isProcessed(t, s, id)
	})

	if rc := countResourceChanges(t, s, corr); rc != 1 {
		t.Fatalf("expected 1 plain delete after expiry, got %d", rc)
	}
	rc := readResourceChange(t, s, corr)
	if rc.ChangeKind != repos.ChangeDelete {
		t.Errorf("expected delete after expiry, got %s", rc.ChangeKind)
	}
}

// TestIntegration_ConcurrentWorkers_NoDoubleProcessing: two processors
// pulling from the same queue must not double-process any row. SKIP
// LOCKED guarantees disjoint claims.
func TestIntegration_ConcurrentWorkers_NoDoubleProcessing(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newStorage(t, url)

	const N = 12
	corrs := make([]uuid.UUID, N)
	for i := 0; i < N; i++ {
		_, corr := insertQueueRow(t, s, "adt", []byte("MSH|x"))
		corrs[i] = corr
	}

	ad := &scriptedAdapter{}
	p1 := newProcessor(t, s, ad, 30*time.Second)
	p2 := newProcessor(t, s, ad, 30*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = p1.Run(ctx) }()
	go func() { defer wg.Done(); _ = p2.Run(ctx) }()

	// Wait for all N to be processed.
	waitFor(t, 10*time.Second, func() bool {
		var unprocessed int
		_ = s.Pool().Pgx().QueryRow(ctx, `SELECT count(*) FROM hl7_message_queue WHERE processed=false`).Scan(&unprocessed)
		return unprocessed == 0
	})
	cancel()
	wg.Wait()

	// Each correlation_id should have exactly one resource_changes row.
	for _, c := range corrs {
		if cnt := countResourceChanges(t, s, c); cnt != 1 {
			t.Errorf("correlation_id %s produced %d resource_changes rows; expected exactly 1", c, cnt)
		}
	}
}

// --- helpers ---

// isProcessed queries hl7_message_queue.processed directly. The
// existing GetByID repo method scans processed_at into **string which
// is incompatible with timestamptz in binary mode; we sidestep it by
// reading just the boolean column.
func isProcessed(t *testing.T, s *storage.Storage, id uuid.UUID) bool {
	t.Helper()
	var processed bool
	if err := s.Pool().Pgx().QueryRow(context.Background(),
		`SELECT processed FROM hl7_message_queue WHERE id=$1`, id,
	).Scan(&processed); err != nil {
		t.Fatalf("isProcessed: %v", err)
	}
	return processed
}

func countResourceChanges(t *testing.T, s *storage.Storage, corr uuid.UUID) int {
	t.Helper()
	var n int
	if err := s.Pool().Pgx().QueryRow(context.Background(),
		`SELECT count(*) FROM resource_changes WHERE correlation_id=$1`, corr,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func readResourceChange(t *testing.T, s *storage.Storage, corr uuid.UUID) repos.ResourceChangeRow {
	t.Helper()
	rows, err := s.Pool().Pgx().Query(context.Background(),
		`SELECT id, sequence, adapter_id, correlation_id, resource_type, change_kind,
		        resource, previous_resource, key_version, occurred_at, event_code,
		        processed, created_month, created_at
		 FROM resource_changes WHERE correlation_id=$1`, corr)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("no resource_changes row for correlation_id %s", corr)
	}
	var rc repos.ResourceChangeRow
	var enc, prev []byte
	var kind string
	if err := rows.Scan(
		&rc.ID, &rc.Sequence, &rc.AdapterID, &rc.CorrelationID,
		&rc.ResourceType, &kind, &enc, &prev, &rc.KeyVersion,
		&rc.OccurredAt, &rc.EventCode, &rc.Processed, &rc.CreatedMonth, &rc.CreatedAt,
	); err != nil {
		t.Fatal(err)
	}
	rc.ChangeKind = repos.ChangeKind(kind)
	rc.Resource = enc
	rc.PreviousResource = prev
	return rc
}

func countPendingPairs(t *testing.T, s *storage.Storage) int {
	t.Helper()
	var n int
	if err := s.Pool().Pgx().QueryRow(context.Background(),
		`SELECT count(*) FROM pending_pairs`,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func countDeadLetters(t *testing.T, s *storage.Storage) int {
	t.Helper()
	var n int
	if err := s.Pool().Pgx().QueryRow(context.Background(),
		`SELECT count(*) FROM dead_letters`,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func readDeadLetters(t *testing.T, s *storage.Storage, sourceID uuid.UUID) []repos.DeadLetterRow {
	t.Helper()
	rows, err := s.Pool().Pgx().Query(context.Background(),
		`SELECT id, kind, source_table, source_id, subscription_id, reason,
		        error_detail, payload_redacted, correlation_id, created_at
		 FROM dead_letters WHERE source_id=$1`, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []repos.DeadLetterRow
	for rows.Next() {
		var r repos.DeadLetterRow
		var subID *uuid.UUID
		var corr *uuid.UUID
		if err := rows.Scan(&r.ID, &r.Kind, &r.SourceTable, &r.SourceID, &subID, &r.Reason,
			&r.ErrorDetail, &r.PayloadRedacted, &corr, &r.CreatedAt); err != nil {
			t.Fatal(err)
		}
		r.SubscriptionID = subID
		r.CorrelationID = corr
		out = append(out, r)
	}
	return out
}
