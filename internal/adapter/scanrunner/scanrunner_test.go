// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package scanrunner_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/scanrunner"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

type fakeRunner struct {
	plan   []spi.ScanTarget
	pages  [][]spi.FhirResource // each call to RunScan returns the next page
	cursor int
	mu     sync.Mutex
}

func (f *fakeRunner) ScanPlan() []spi.ScanTarget { return f.plan }

func (f *fakeRunner) RunScan(_ context.Context, _ spi.ScanTarget) (spi.ScanIterator, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cursor >= len(f.pages) {
		return &fakeIter{}, nil
	}
	page := f.pages[f.cursor]
	f.cursor++
	return &fakeIter{items: page}, nil
}

func (f *fakeRunner) ContentHash(r spi.FhirResource) string {
	sum := sha256.Sum256(r.Body)
	return hex.EncodeToString(sum[:])
}

func (f *fakeRunner) Normalize(r spi.FhirResource) spi.FhirResource { return r }

type fakeIter struct {
	items []spi.FhirResource
	pos   int
}

func (i *fakeIter) Next(_ context.Context) (spi.FhirResource, bool, error) {
	if i.pos >= len(i.items) {
		return spi.FhirResource{}, false, nil
	}
	r := i.items[i.pos]
	i.pos++
	return r, true, nil
}

type recorderSink struct {
	mu   sync.Mutex
	rows []repos.ResourceChangeRow
}

func (s *recorderSink) Insert(_ context.Context, row repos.ResourceChangeRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, row)
	return nil
}

func (s *recorderSink) Snapshot() []repos.ResourceChangeRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]repos.ResourceChangeRow, len(s.rows))
	copy(out, s.rows)
	return out
}

// P2.1: a first-sighting resource emits a `create` ChangeKind.
func TestScanRunner_FirstSightingIsCreate(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{
		plan: []spi.ScanTarget{{ResourceType: "ServiceRequest", Cadence: time.Hour}},
		pages: [][]spi.FhirResource{
			{{ResourceType: "ServiceRequest", ID: "abc", Body: []byte(`{"resourceType":"ServiceRequest","id":"abc","status":"active"}`)}},
		},
	}
	sink := &recorderSink{}
	w, err := scanrunner.New(scanrunner.Options{
		AdapterID: "vendorA",
		Runner:    runner,
		Sink:      sink,
		Clock:     func() time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := w.TickOne(context.Background(), runner.plan[0]); err != nil {
		t.Fatalf("TickOne: %v", err)
	}
	rows := sink.Snapshot()
	if len(rows) != 1 {
		t.Fatalf("rows: want 1, got %d", len(rows))
	}
	if rows[0].ChangeKind != repos.ChangeKind(spi.ChangeCreate) {
		t.Errorf("kind: want create, got %q", rows[0].ChangeKind)
	}
	if rows[0].AdapterID != "vendorA" {
		t.Errorf("adapter: %q", rows[0].AdapterID)
	}
}

// P2.1: a second tick that yields the same resource with the same
// content hash does NOT emit a row (dedup).
func TestScanRunner_SameContentDeduped(t *testing.T) {
	t.Parallel()
	body := []byte(`{"resourceType":"ServiceRequest","id":"abc","status":"active"}`)
	runner := &fakeRunner{
		plan: []spi.ScanTarget{{ResourceType: "ServiceRequest", Cadence: time.Hour}},
		pages: [][]spi.FhirResource{
			{{ResourceType: "ServiceRequest", ID: "abc", Body: body}},
			{{ResourceType: "ServiceRequest", ID: "abc", Body: body}},
		},
	}
	sink := &recorderSink{}
	w, _ := scanrunner.New(scanrunner.Options{Runner: runner, Sink: sink})
	if err := w.TickOne(context.Background(), runner.plan[0]); err != nil {
		t.Fatalf("tick1: %v", err)
	}
	if err := w.TickOne(context.Background(), runner.plan[0]); err != nil {
		t.Fatalf("tick2: %v", err)
	}
	if got := len(sink.Snapshot()); got != 1 {
		t.Fatalf("rows: want 1 (second tick must dedup), got %d", got)
	}
}

// P2.1: a second tick that yields the same id with different content
// emits an `update` ChangeKind.
func TestScanRunner_ContentChangeIsUpdate(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{
		plan: []spi.ScanTarget{{ResourceType: "ServiceRequest", Cadence: time.Hour}},
		pages: [][]spi.FhirResource{
			{{ResourceType: "ServiceRequest", ID: "abc", Body: []byte(`{"resourceType":"ServiceRequest","id":"abc","status":"draft"}`)}},
			{{ResourceType: "ServiceRequest", ID: "abc", Body: []byte(`{"resourceType":"ServiceRequest","id":"abc","status":"active"}`)}},
		},
	}
	sink := &recorderSink{}
	w, _ := scanrunner.New(scanrunner.Options{Runner: runner, Sink: sink})
	if err := w.TickOne(context.Background(), runner.plan[0]); err != nil {
		t.Fatalf("tick1: %v", err)
	}
	if err := w.TickOne(context.Background(), runner.plan[0]); err != nil {
		t.Fatalf("tick2: %v", err)
	}
	rows := sink.Snapshot()
	if len(rows) != 2 {
		t.Fatalf("rows: want 2, got %d", len(rows))
	}
	if rows[0].ChangeKind != repos.ChangeKind(spi.ChangeCreate) {
		t.Errorf("first: want create, got %q", rows[0].ChangeKind)
	}
	if rows[1].ChangeKind != repos.ChangeKind(spi.ChangeUpdate) {
		t.Errorf("second: want update, got %q", rows[1].ChangeKind)
	}
}

// P2.1: a runner with an empty plan does not error and does not emit.
func TestScanRunner_EmptyPlanIdles(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{plan: nil}
	sink := &recorderSink{}
	w, _ := scanrunner.New(scanrunner.Options{Runner: runner, Sink: sink})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(sink.Snapshot()); got != 0 {
		t.Errorf("emitted with empty plan: %d", got)
	}
}

// P2.1: New rejects missing Runner / Sink.
func TestScanRunner_NewValidates(t *testing.T) {
	t.Parallel()
	if _, err := scanrunner.New(scanrunner.Options{Sink: &recorderSink{}}); err == nil {
		t.Errorf("expected error: missing Runner")
	}
	if _, err := scanrunner.New(scanrunner.Options{Runner: &fakeRunner{}}); err == nil {
		t.Errorf("expected error: missing Sink")
	}
}
