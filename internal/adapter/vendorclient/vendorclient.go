// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package vendorclient is the production worker for the
// VendorAPIClient SPI (P2.2). It hands the adapter an EventSink and
// drives Consume; the adapter's Consume loop calls Push on the sink
// for every vendor record. The worker translates each record (via
// VendorAPIClient.Translate) into a ResourceChange and persists it
// through the resource_changes repo so the matcher worker picks it
// up on next tick.
//
// MVP scope:
//   - Single-instance, no leader election.
//   - One Consume loop per worker, restarted with exponential backoff
//     on error (capped at MaxBackoff).
//   - Cursor is in-memory only — on restart, Consume is called with
//     the operator-supplied initial cursor. Persisting the cursor to
//     Postgres so the worker resumes mid-stream is a post-MVP
//     follow-up; for v1.0 each vendor adapter that needs durable
//     resume can wrap the SPI itself.
//
// Out of scope (post-MVP):
//   - Persistent cursor store (per LLD: adapter_state table)
//   - Supervisor restart-on-panic with per-adapter labels
//   - Multi-instance partitioning (one Consume loop owns the entire
//     vendor stream)
//   - Dead-letter on persistent translate failures
package vendorclient

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

// RowSink mirrors scanrunner.RowSink — narrow contract for tests.
type RowSink interface {
	Insert(ctx context.Context, row repos.ResourceChangeRow) error
}

// repoSink adapts the repo + Querier into RowSink.
type repoSink struct {
	repo *repos.ResourceChangesRepo
	q    repos.Querier
}

// Insert satisfies RowSink.
func (s *repoSink) Insert(ctx context.Context, row repos.ResourceChangeRow) error {
	if s.repo == nil || s.q == nil {
		return errors.New("vendorclient: repo or querier not configured")
	}
	_, _, err := s.repo.Insert(ctx, s.q, row)
	return err
}

// NewRepoSink wraps a repos.ResourceChangesRepo + Querier into a
// RowSink. Production wiring calls this.
func NewRepoSink(repo *repos.ResourceChangesRepo, q repos.Querier) RowSink {
	return &repoSink{repo: repo, q: q}
}

// Worker drives one VendorAPIClient. Construct via New.
type Worker struct {
	adapterID string
	client    spi.VendorAPIClient
	sink      RowSink
	clock     func() time.Time

	initialCursor   []byte
	backoffInitial  time.Duration
	backoffMax      time.Duration

	mu     sync.Mutex
	cursor []byte
}

// Options configures New.
type Options struct {
	// AdapterID is the value persisted on resource_changes.adapter_id.
	AdapterID string
	// Client is the adapter-supplied VendorAPIClient. Required.
	Client spi.VendorAPIClient
	// Sink is the destination for emitted rows. Required.
	Sink RowSink
	// Clock is the time source; nil = time.Now.
	Clock func() time.Time
	// InitialCursor is the cursor passed to the first Consume call.
	// Operators that want to start at "now" pass nil; replays pass
	// the persisted cursor.
	InitialCursor []byte
	// BackoffInitial / BackoffMax bound the restart-after-error
	// delay. Defaults: 1s / 60s.
	BackoffInitial time.Duration
	BackoffMax     time.Duration
}

// New constructs a Worker.
func New(opts Options) (*Worker, error) {
	if opts.Client == nil {
		return nil, errors.New("vendorclient: Client is required")
	}
	if opts.Sink == nil {
		return nil, errors.New("vendorclient: Sink is required")
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	bi := opts.BackoffInitial
	if bi <= 0 {
		bi = time.Second
	}
	bm := opts.BackoffMax
	if bm <= 0 {
		bm = 60 * time.Second
	}
	return &Worker{
		adapterID:      opts.AdapterID,
		client:         opts.Client,
		sink:           opts.Sink,
		clock:          clock,
		initialCursor:  append([]byte(nil), opts.InitialCursor...),
		cursor:         append([]byte(nil), opts.InitialCursor...),
		backoffInitial: bi,
		backoffMax:     bm,
	}, nil
}

// eventSink is the SPI sink the worker hands to the adapter. Each
// Push translates the record and persists a row through the
// underlying RowSink, then advances the cursor.
type eventSink struct {
	w *Worker
}

// Push satisfies spi.EventSink.
func (s *eventSink) Push(ctx context.Context, record spi.VendorRecord) error {
	change, err := s.w.client.Translate(record)
	if err != nil {
		return fmt.Errorf("vendorclient: translate: %w", err)
	}
	if change.CorrelationID == uuid.Nil {
		change.CorrelationID = uuid.New()
	}
	if change.OccurredAt.IsZero() {
		change.OccurredAt = s.w.clock()
	}
	if err := change.Validate(); err != nil {
		return fmt.Errorf("vendorclient: invalid ResourceChange: %w", err)
	}
	row := repos.ResourceChangeRow{
		AdapterID:     s.w.adapterID,
		CorrelationID: change.CorrelationID,
		ResourceType:  change.ResourceType,
		ChangeKind:    repos.ChangeKind(change.ChangeKind),
		Resource:      change.Resource.Body,
		OccurredAt:    change.OccurredAt,
		EventCode:     change.EventCode,
		CreatedMonth:  change.OccurredAt.UTC().Truncate(24 * time.Hour),
	}
	if change.PreviousResource != nil {
		row.PreviousResource = change.PreviousResource.Body
	}
	if err := s.w.sink.Insert(ctx, row); err != nil {
		return fmt.Errorf("vendorclient: insert: %w", err)
	}
	if len(record.Cursor) > 0 {
		s.w.mu.Lock()
		s.w.cursor = append([]byte(nil), record.Cursor...)
		s.w.mu.Unlock()
	}
	return nil
}

// Run blocks until ctx is canceled. Each iteration calls
// VendorAPIClient.Consume with the current cursor. On Consume error
// the worker waits (exponential backoff) and retries; on ctx.Done it
// returns nil.
func (w *Worker) Run(ctx context.Context) error {
	sink := &eventSink{w: w}
	backoff := w.backoffInitial
	for {
		if ctx.Err() != nil {
			return nil
		}
		w.mu.Lock()
		cur := append([]byte(nil), w.cursor...)
		w.mu.Unlock()
		err := w.client.Consume(ctx, sink, cur)
		if err == nil {
			// Consume returned nil — vendor stream closed cleanly. The
			// adapter is expected to reconnect from inside Consume; if
			// it returned nil it means "stream ended". Sleep and retry.
			backoff = w.backoffInitial
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(w.backoffInitial):
			}
			continue
		}
		// Consume errored. Wait, then retry with backoff.
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = nextBackoff(backoff, w.backoffMax)
	}
}

// Cursor returns the worker's most-recently-seen cursor. Exposed so
// tests (and a future durable-cursor implementation) can observe
// progress.
func (w *Worker) Cursor() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.cursor...)
}

// PushOne is the test-friendly seam: it bypasses Consume and pushes
// one VendorRecord directly through the worker's EventSink. Used by
// unit tests that want to assert on the translation + insert path
// without writing a fake Consume loop.
func (w *Worker) PushOne(ctx context.Context, record spi.VendorRecord) error {
	return (&eventSink{w: w}).Push(ctx, record)
}

func nextBackoff(cur, ceiling time.Duration) time.Duration {
	next := cur * 2
	if next > ceiling {
		return ceiling
	}
	return next
}
