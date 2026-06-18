// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package scanrunner is the production worker for the FhirScanRunner SPI
// (P2.1). It periodically asks the adapter for its scan plan, runs each
// scan, and emits one resource_changes row per yielded FHIR resource.
//
// MVP scope:
//   - Single-instance, no leader election (matches ADR 0002)
//   - Each scan target runs on its declared Cadence using a per-target
//     ticker. New targets surface via ScanPlan() at startup; live
//     re-planning is deferred.
//   - ContentHash is the dedup key — successive scans that produce the
//     same hash on the same (resourceType, id) do NOT emit a new row,
//     so the matcher does not see synthetic update events.
//   - First-sighting heuristic: a (resourceType, id) tuple that the
//     worker has not seen before emits a `create` ChangeKind. Anything
//     it has seen before with a different ContentHash emits an `update`.
//     Deletes are post-MVP (the SPI's current ScanIterator does not
//     surface deletions).
//   - In-memory hash cache keyed by (adapterID, resourceType, id). On
//     restart the cache is cold; the first scan post-restart re-emits
//     every resource as `create`. Persisting the cache to Postgres is
//     a post-MVP follow-up.
//
// Out of scope (post-MVP):
//   - Persistent ContentHash store (so the cache survives restarts)
//   - etag / Last-Modified / conditional GET
//   - Supervisor restart-on-panic with per-adapter labels (LLD framework)
//   - Multi-instance partitioning of scan targets
//   - DELETE detection (requires a separate "tombstone" SPI)
package scanrunner

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// RowSink is the narrow contract the worker uses to persist rows.
// Production wiring satisfies it with a *repos.ResourceChangesRepo
// bound to a *pgxpool.Pool; unit tests satisfy it with a slice
// recorder. Decoupling the worker from the full repo + pool stack
// keeps the logic tests free of testcontainers.
type RowSink interface {
	Insert(ctx context.Context, row repos.ResourceChangeRow) error
}

// repoSink adapts a *repos.ResourceChangesRepo + a Querier into the
// RowSink shape. The wiring layer constructs this with the production
// pool.
type repoSink struct {
	repo *repos.ResourceChangesRepo
	q    repos.Querier
}

// Insert satisfies RowSink.
func (s *repoSink) Insert(ctx context.Context, row repos.ResourceChangeRow) error {
	if s.repo == nil || s.q == nil {
		return errors.New("scanrunner: repo or querier not configured")
	}
	_, _, err := s.repo.Insert(ctx, s.q, row)
	return err
}

// NewRepoSink wraps a repos.ResourceChangesRepo + Querier into a
// RowSink. Production wiring calls this.
func NewRepoSink(repo *repos.ResourceChangesRepo, q repos.Querier) RowSink {
	return &repoSink{repo: repo, q: q}
}

// Worker drives one FhirScanRunner. Construct via New.
type Worker struct {
	adapterID string
	runner    spi.FhirScanRunner
	sink      RowSink
	clock     func() time.Time

	// hashCache keys ContentHash by (resourceType, id) so a scan that
	// produces the same hash on a previously-seen resource does not
	// emit a duplicate row. See package doc note about cold cache on
	// restart.
	mu        sync.Mutex
	hashCache map[string]string // key: resourceType+"|"+id, value: ContentHash
}

// Options configures New.
type Options struct {
	// AdapterID is the value persisted on resource_changes.adapter_id
	// for every emitted row. Operators see this as the source label
	// in dashboards.
	AdapterID string
	// Runner is the adapter-supplied FhirScanRunner. Required.
	Runner spi.FhirScanRunner
	// Sink is the destination for emitted rows. Production wiring
	// constructs via NewRepoSink; tests pass a slice recorder.
	Sink RowSink
	// Clock is the time source; nil = time.Now.
	Clock func() time.Time
}

// New constructs a Worker.
func New(opts Options) (*Worker, error) {
	if opts.Runner == nil {
		return nil, errors.New("scanrunner: Runner is required")
	}
	if opts.Sink == nil {
		return nil, errors.New("scanrunner: Sink is required")
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Worker{
		adapterID: opts.AdapterID,
		runner:    opts.Runner,
		sink:      opts.Sink,
		clock:     clock,
		hashCache: map[string]string{},
	}, nil
}

// Run blocks until ctx is canceled. It launches a per-target ticker
// driven by ScanTarget.Cadence, calls RunScan on each tick, and
// writes one resource_changes row per yielded resource (modulo the
// ContentHash dedup gate). Returns nil on clean shutdown; an error
// only if ScanPlan returns nothing useful.
func (w *Worker) Run(ctx context.Context) error {
	plan := w.runner.ScanPlan()
	if len(plan) == 0 {
		// No targets: idle. Match storage workers' behavior — return
		// when ctx cancels rather than failing.
		<-ctx.Done()
		return nil
	}
	var wg sync.WaitGroup
	for i := range plan {
		t := plan[i]
		if t.Cadence <= 0 {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.runOne(ctx, t)
		}()
	}
	wg.Wait()
	return nil
}

// runOne runs a single ScanTarget on its declared Cadence. Each tick
// is one full pagination over the iterator the adapter returns. A
// ScanIterator that errors mid-page logs the error (in production:
// emits a metric + breaks the tick); the next tick retries.
func (w *Worker) runOne(ctx context.Context, t spi.ScanTarget) {
	// Run immediately on entry, then on each tick. Most operators
	// want "scan on startup, scan every N minutes thereafter."
	w.tickOne(ctx, t)
	tk := time.NewTicker(t.Cadence)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			w.tickOne(ctx, t)
		}
	}
}

// TickOne runs one scan iteration of target t. Exported so tests can
// drive the worker deterministically without a long-lived ticker.
func (w *Worker) TickOne(ctx context.Context, t spi.ScanTarget) error {
	return w.tickOne(ctx, t)
}

func (w *Worker) tickOne(ctx context.Context, t spi.ScanTarget) error {
	iter, err := w.runner.RunScan(ctx, t)
	if err != nil {
		return fmt.Errorf("scanrunner: RunScan: %w", err)
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		res, ok, err := iter.Next(ctx)
		if err != nil {
			return fmt.Errorf("scanrunner: iterator: %w", err)
		}
		if !ok {
			return nil
		}
		// Apply the adapter's normalization before hashing so two
		// snapshots that differ only in profile-irrelevant fields hash
		// the same.
		norm := w.runner.Normalize(res)
		hash := w.runner.ContentHash(norm)
		key := norm.ResourceType + "|" + norm.ID

		w.mu.Lock()
		prev, seen := w.hashCache[key]
		w.mu.Unlock()
		if seen && prev == hash {
			// No-op: same content, no row to write.
			continue
		}

		kind := spi.ChangeUpdate
		if !seen {
			kind = spi.ChangeCreate
		}
		row := repos.ResourceChangeRow{
			AdapterID:     w.adapterID,
			CorrelationID: uuid.New(),
			ResourceType:  norm.ResourceType,
			ChangeKind:    repos.ChangeKind(kind),
			Resource:      norm.Body,
			OccurredAt:    w.clock(),
			CreatedMonth:  w.clock().UTC().Truncate(24 * time.Hour),
		}
		if err := w.sink.Insert(ctx, row); err != nil {
			return fmt.Errorf("scanrunner: insert: %w", err)
		}
		w.mu.Lock()
		w.hashCache[key] = hash
		w.mu.Unlock()
	}
}
