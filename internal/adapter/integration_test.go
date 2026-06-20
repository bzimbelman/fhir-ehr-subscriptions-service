//go:build integration

// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package integration_test holds the wired-path integration tests for
// the adapter sub-packages — scanrunner, vendorclient, supervisor.
//
// OP #153 (audit Finding #101): the per-package tests at
// `internal/adapter/{scanrunner,vendorclient,supervisor}/*_test.go`,
// `internal/hydration/hydration_test.go`, and
// `internal/webhook/webhook_test.go` each inject a fake of the very
// interface the package consumes — they prove the loop / state machine
// of the package, not that the package wires correctly to a real
// repo + Postgres + adapter. This file holds the integration test that
// pins the missing seam: a real `scanrunner.Worker`, fed by a fake
// `spi.FhirScanRunner` implementation, persists rows into a real
// Postgres `resource_changes` table via the production
// `repos.ResourceChangesRepo` and `storage.Storage` stack — exactly
// the wiring `cmd/fhir-subs/wiring.go` builds at startup.
//
// Requires Docker for the testcontainers Postgres. Skips gracefully
// when Docker is unavailable.
//
// Run with: go test -race -tags integration ./internal/adapter/...

package adapter_integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/scanrunner"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage"
)

// startPostgres spins up a Postgres 16 container or t.Skips when
// Docker is unavailable. Same shape as the matcher / hl7processor
// integration tests; the panic-recover guards against the
// testcontainers MustExtractDockerHost path that panics instead of
// returning a clean error.
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
			tcpostgres.WithDatabase("adapter_test"),
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

// newTestStorage stands up a fully migrated *storage.Storage against
// the testcontainer DSN. Same key-version setup as the matcher
// integration test so resource bodies round-trip through the codec.
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

// fakeAdapter is a minimal spi.FhirScanRunner: it returns one fixed
// scan plan and replays a small batch of FHIR resources on each
// RunScan call. ContentHash is sha256(Body) so dedup behaves like
// the production default.
//
// This is the "fake adapter" the AC calls for — it stands in for a
// real EHR scan source. The test asserts that the rest of the chain
// (scanrunner.Worker → repos.ResourceChangesRepo → Postgres) is real.
type fakeAdapter struct {
	plan      []spi.ScanTarget
	resources []spi.FhirResource
}

func (f *fakeAdapter) ScanPlan() []spi.ScanTarget { return f.plan }

func (f *fakeAdapter) RunScan(_ context.Context, _ spi.ScanTarget) (spi.ScanIterator, error) {
	dup := make([]spi.FhirResource, len(f.resources))
	copy(dup, f.resources)
	return &fakeIter{items: dup}, nil
}

func (f *fakeAdapter) ContentHash(r spi.FhirResource) string {
	sum := sha256.Sum256(r.Body)
	return hex.EncodeToString(sum[:])
}

func (f *fakeAdapter) Normalize(r spi.FhirResource) spi.FhirResource { return r }

type fakeIter struct {
	items []spi.FhirResource
	pos   int
}

func (it *fakeIter) Next(_ context.Context) (spi.FhirResource, bool, error) {
	if it.pos >= len(it.items) {
		return spi.FhirResource{}, false, nil
	}
	r := it.items[it.pos]
	it.pos++
	return r, true, nil
}

func (it *fakeIter) Close() error { return nil }

// TestAdapterIntegration_ScanRunnerWiresThroughToResourceChanges is
// the wired-path test the audit (Finding #101) called for. It:
//
//  1. Starts a real Postgres + applies migrations via storage.Start.
//  2. Constructs a real scanrunner.Worker with a real RowSink built
//     from the production repos.ResourceChangesRepo.
//  3. Uses a fakeAdapter to source a known batch of FHIR resources.
//  4. Drives one tick.
//  5. Asserts rows landed in resource_changes via the SAME repo
//     (no SELECT 1 trick — we use ClaimUnprocessed which is the
//     production read path).
//
// The point isn't to exercise scanrunner's loop logic — the per-
// package unit test still does that. The point is to prove that the
// wiring is real: that scanrunner constructs the row, the repo
// encrypts the body under the active codec key, the migration's
// CHECK constraints accept the kind, and ClaimUnprocessed reads back
// what was written.
func TestAdapterIntegration_ScanRunnerWiresThroughToResourceChanges(t *testing.T) {
	url := startPostgres(t)
	st := newTestStorage(t, url)

	rcs := st.ResourceChanges()
	if rcs == nil {
		t.Fatalf("storage.ResourceChanges() returned nil — wiring drift")
	}

	target := spi.ScanTarget{
		ResourceType: "Patient",
		Cadence:      time.Minute, // unused; we drive TickOne directly
	}
	body1 := []byte(`{"resourceType":"Patient","id":"p1"}`)
	body2 := []byte(`{"resourceType":"Patient","id":"p2"}`)
	adapter := &fakeAdapter{
		plan: []spi.ScanTarget{target},
		resources: []spi.FhirResource{
			{ResourceType: "Patient", ID: "p1", Body: body1},
			{ResourceType: "Patient", ID: "p2", Body: body2},
		},
	}

	pool := st.Pool().Pgx()
	sink := scanrunner.NewRepoSink(rcs, pool)

	w, err := scanrunner.New(scanrunner.Options{
		AdapterID: "fake-ehr",
		Runner:    adapter,
		Sink:      sink,
	})
	if err != nil {
		t.Fatalf("scanrunner.New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := w.TickOne(ctx, target); err != nil {
		t.Fatalf("TickOne: %v", err)
	}

	// Read back via the same repo — the production claim path.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	rows, err := rcs.ClaimUnprocessed(ctx, tx, 10)
	if err != nil {
		t.Fatalf("ClaimUnprocessed: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (one per fake-adapter resource)", len(rows))
	}

	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.ResourceType+"/"+string(row.ChangeKind)] = true
		if row.AdapterID != "fake-ehr" {
			t.Errorf("AdapterID = %q, want fake-ehr", row.AdapterID)
		}
		if row.CorrelationID == uuid.Nil {
			t.Errorf("CorrelationID was zero — repo did not assign one")
		}
		// Body must round-trip through the codec untouched.
		if got := string(row.Resource); got != string(body1) && got != string(body2) {
			t.Errorf("resource body decode mismatch: %q", got)
		}
	}
	if !seen["Patient/create"] {
		t.Errorf("expected at least one Patient/create row; got %v", seen)
	}

	// A second tick with the SAME inputs MUST be a no-op (ContentHash
	// dedup gate). This pins the seam between the worker's hash cache
	// and the repo write path: dedup is in the worker, not the DB.
	if err := w.TickOne(ctx, target); err != nil {
		t.Fatalf("second TickOne: %v", err)
	}

	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin 2: %v", err)
	}
	defer tx2.Rollback(ctx)
	more, err := rcs.ClaimUnprocessed(ctx, tx2, 10)
	if err != nil {
		t.Fatalf("ClaimUnprocessed 2: %v", err)
	}
	if len(more) != 0 {
		t.Fatalf("second tick wrote %d rows; expected 0 (dedup gate must hold)", len(more))
	}
}
