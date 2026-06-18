// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package observability_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
)

func TestStart_DefaultsAreSane(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	mod, handles, err := observability.Start(context.Background(), observability.Config{
		Logging: observability.LoggingConfig{Level: "info", Format: "json"},
	}, observability.Context{
		StoragePool: store,
		Clock:       func() time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = mod.Shutdown(context.Background()) }()

	if handles.Metrics == nil || handles.Tracer == nil || handles.Logger == nil || handles.Audit == nil {
		t.Fatalf("missing handle: %+v", handles)
	}

	// Tracer is disabled when no OTLP endpoint is configured (LLD §11).
	if !handles.Tracer.Disabled() {
		t.Fatalf("expected disabled tracer when OTLPEndpoint is empty")
	}

	// Audit Emit hits the durable store.
	err = handles.Audit.Emit(context.Background(), observability.AuditEvent{
		ActorKind: "system",
		Action:    "test",
		Outcome:   "success",
	})
	if err != nil {
		t.Fatalf("audit emit: %v", err)
	}
	if len(store.snapshot()) != 1 {
		t.Fatalf("expected 1 audit row")
	}

	// /metrics endpoint serves the inventory.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	mod.PrometheusHandler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200; got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"fhir_subs_observability_metrics_registered",
		"fhir_subs_audit_writes_total",
		"fhir_subs_audit_chain_invalid_total",
		"fhir_subs_audit_sink_failures_total",
		"fhir_subs_logging_phi_dropped_total",
		"fhir_subs_dead_letters_total",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("/metrics missing %q in: %s", want, body)
		}
	}
}

func TestStart_RequiresStoragePool(t *testing.T) {
	t.Parallel()
	_, _, err := observability.Start(context.Background(), observability.Config{}, observability.Context{})
	if err == nil {
		t.Fatalf("expected error")
	}
}

// fakeStore mirrors the audit/audit_test.go fake at module scope so the
// observability_test package can exercise Start end-to-end.
type fakeStore struct {
	chain sync.Mutex
	mu    sync.Mutex
	rows  []*audit.Row
}

func newFakeStore() *fakeStore { return &fakeStore{} }

func (s *fakeStore) InsertAuditRow(_ context.Context, row audit.Row) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := row
	s.rows = append(s.rows, &r)
	return nil
}

func (s *fakeStore) LastChainHash(_ context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rows) == 0 {
		return nil, nil
	}
	return s.rows[len(s.rows)-1].ChainHash, nil
}

func (s *fakeStore) AcquireChainLock(_ context.Context) (func() error, error) {
	s.chain.Lock()
	return func() error { s.chain.Unlock(); return nil }, nil
}

func (s *fakeStore) IterateRows(_ context.Context, fn func(audit.Row) error) error {
	s.mu.Lock()
	rows := append([]*audit.Row(nil), s.rows...)
	s.mu.Unlock()
	for _, r := range rows {
		if err := fn(*r); err != nil {
			return err
		}
	}
	return nil
}

func (s *fakeStore) snapshot() []audit.Row {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]audit.Row, len(s.rows))
	for i, r := range s.rows {
		out[i] = *r
	}
	return out
}
