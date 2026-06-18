// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package vendorclient_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/vendorclient"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

type fakeClient struct {
	mu          sync.Mutex
	consumeFn   func(ctx context.Context, sink spi.EventSink, cursor []byte) error
	translateFn func(record spi.VendorRecord) (spi.ResourceChange, error)
}

func (f *fakeClient) Consume(ctx context.Context, sink spi.EventSink, cursor []byte) error {
	f.mu.Lock()
	fn := f.consumeFn
	f.mu.Unlock()
	if fn == nil {
		<-ctx.Done()
		return nil
	}
	return fn(ctx, sink, cursor)
}

func (f *fakeClient) Translate(record spi.VendorRecord) (spi.ResourceChange, error) {
	f.mu.Lock()
	fn := f.translateFn
	f.mu.Unlock()
	if fn == nil {
		// Default translation: pass-through if Payload is a [3]string of
		// (resourceType, id, body); otherwise fail.
		t, ok := record.Payload.([3]string)
		if !ok {
			return spi.ResourceChange{}, errors.New("default translate needs [3]string payload")
		}
		return spi.ResourceChange{
			ResourceType: t[0],
			ChangeKind:   spi.ChangeUpdate,
			Resource: spi.FhirResource{
				ResourceType: t[0],
				ID:           t[1],
				Body:         []byte(t[2]),
			},
			CorrelationID: uuid.New(),
			EventCode:     record.EventCode,
		}, nil
	}
	return fn(record)
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

// P2.2: PushOne translates the vendor record, persists a row, and
// advances the cursor.
func TestVendorClient_PushOneTranslatesAndPersists(t *testing.T) {
	t.Parallel()
	client := &fakeClient{}
	sink := &recorderSink{}
	w, err := vendorclient.New(vendorclient.Options{
		AdapterID: "vendorA",
		Client:    client,
		Sink:      sink,
		Clock:     func() time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := spi.VendorRecord{
		Cursor:    []byte("cursor-1"),
		Payload:   [3]string{"ServiceRequest", "abc", `{"resourceType":"ServiceRequest","id":"abc","status":"active"}`},
		EventCode: "ORM-O01",
	}
	if err := w.PushOne(context.Background(), rec); err != nil {
		t.Fatalf("PushOne: %v", err)
	}

	rows := sink.Snapshot()
	if len(rows) != 1 {
		t.Fatalf("rows: want 1, got %d", len(rows))
	}
	r := rows[0]
	if r.AdapterID != "vendorA" {
		t.Errorf("adapter: %q", r.AdapterID)
	}
	if r.ResourceType != "ServiceRequest" {
		t.Errorf("resource type: %q", r.ResourceType)
	}
	if string(r.Resource) == "" {
		t.Errorf("resource body empty")
	}
	if r.EventCode != "ORM-O01" {
		t.Errorf("event code: %q", r.EventCode)
	}

	if string(w.Cursor()) != "cursor-1" {
		t.Errorf("cursor: %q", string(w.Cursor()))
	}
}

// P2.2: a translate error surfaces as a Push error and does NOT
// advance the cursor or persist a row.
func TestVendorClient_TranslateErrorDoesNotPersist(t *testing.T) {
	t.Parallel()
	client := &fakeClient{
		translateFn: func(_ spi.VendorRecord) (spi.ResourceChange, error) {
			return spi.ResourceChange{}, errors.New("boom")
		},
	}
	sink := &recorderSink{}
	w, _ := vendorclient.New(vendorclient.Options{Client: client, Sink: sink})

	err := w.PushOne(context.Background(), spi.VendorRecord{Cursor: []byte("c1")})
	if err == nil {
		t.Errorf("expected error from PushOne")
	}
	if got := len(sink.Snapshot()); got != 0 {
		t.Errorf("must not persist on translate error: %d", got)
	}
	if len(w.Cursor()) != 0 {
		t.Errorf("cursor must not advance on translate error: %q", w.Cursor())
	}
}

// P2.2: Run drives Consume in a loop, surfaces records to the sink,
// and exits cleanly when ctx is canceled.
func TestVendorClient_RunDrivesConsume(t *testing.T) {
	t.Parallel()
	consumed := make(chan struct{})
	client := &fakeClient{
		consumeFn: func(ctx context.Context, sink spi.EventSink, cursor []byte) error {
			rec := spi.VendorRecord{
				Cursor:  []byte("c1"),
				Payload: [3]string{"ServiceRequest", "abc", `{"resourceType":"ServiceRequest","id":"abc"}`},
			}
			if err := sink.Push(ctx, rec); err != nil {
				return err
			}
			close(consumed)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	sink := &recorderSink{}
	w, _ := vendorclient.New(vendorclient.Options{Client: client, Sink: sink})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	select {
	case <-consumed:
	case <-time.After(150 * time.Millisecond):
		t.Fatal("Consume never ran")
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run: %v", err)
	}
	if got := len(sink.Snapshot()); got != 1 {
		t.Errorf("rows: %d", got)
	}
}

// P2.2: New rejects missing Client / Sink.
func TestVendorClient_NewValidates(t *testing.T) {
	t.Parallel()
	if _, err := vendorclient.New(vendorclient.Options{Sink: &recorderSink{}}); err == nil {
		t.Errorf("expected error: missing Client")
	}
	if _, err := vendorclient.New(vendorclient.Options{Client: &fakeClient{}}); err == nil {
		t.Errorf("expected error: missing Sink")
	}
}
