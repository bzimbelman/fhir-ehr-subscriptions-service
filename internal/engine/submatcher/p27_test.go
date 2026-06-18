//go:build integration

// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package submatcher_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/submatcher"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

type stubAuthRecheckerInt struct {
	calls   int32
	revoked map[string]bool
}

func (s *stubAuthRecheckerInt) Recheck(_ context.Context, _, subID string) (bool, error) {
	atomic.AddInt32(&s.calls, 1)
	if s.revoked[subID] {
		return false, nil
	}
	return true, nil
}

type errAuthRechecker struct{}

func (errAuthRechecker) Recheck(context.Context, string, string) (bool, error) {
	return false, errors.New("auth store offline")
}

type recordingStateUpdater struct {
	calls   int32
	subID   uuid.UUID
	reason  string
	failErr error
}

func (r *recordingStateUpdater) MarkErrorRevoked(_ context.Context, _ pgx.Tx, sub uuid.UUID, reason string) error {
	atomic.AddInt32(&r.calls, 1)
	r.subID = sub
	r.reason = reason
	return r.failErr
}

// P2.7: when the AuthRechecker returns Revoked the worker:
//   - emits FanoutAuthRevoked instead of FanoutMatch
//   - skips the deliveries insert
//   - calls MarkErrorRevoked on the SubscriptionStateUpdater
func TestIntegrationP27_RevokedSuppressesDeliveryAndMarksError(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	seedAuthClient(t, s, "client-revoked")
	subID, err := s.Subscriptions().Insert(ctx, s.Pool().Pgx(), repos.SubscriptionRow{
		ClientID:    "client-revoked",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/order-changed",
		ChannelType: "rest-hook",
		Endpoint:    "https://sub.example.org/notif",
		FilterBy:    filterByJSON(map[string]string{"filterParameter": "patient", "value": "Patient/123"}),
		Content:     "id-only",
	})
	if err != nil {
		t.Fatalf("insert sub: %v", err)
	}
	rcID, _ := seedEhrEvent(t, s, "http://example.org/order-changed", "ServiceRequest/abc",
		[]byte(`{"resourceType":"ServiceRequest","id":"abc","subject":{"reference":"Patient/123"},"status":"active"}`),
	)

	rec := &stubAuthRecheckerInt{revoked: map[string]bool{subID.String(): true}}
	upd := &recordingStateUpdater{}

	w := submatcher.NewWorker(s.Pool().Pgx(), s.Subscriptions(), s.EhrEvents(), s.Deliveries(), submatcher.Config{ClaimBatchSize: 1},
		submatcher.WithAuthRechecker(rec),
		submatcher.WithStateUpdater(upd),
	)
	processed, err := w.TickOnce(ctx)
	if err != nil {
		t.Fatalf("TickOnce: %v", err)
	}
	if !processed {
		t.Fatal("expected processed=true (one row pending)")
	}

	// No deliveries row for the revoked subscription.
	var n int
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT count(*) FROM deliveries WHERE ehr_event_id=$1 AND subscription_id=$2`, rcID, subID,
	).Scan(&n); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if n != 0 {
		t.Fatalf("revoked subscription must not get a deliveries row, got %d", n)
	}

	if got := atomic.LoadInt32(&rec.calls); got != 1 {
		t.Errorf("recheck calls: want 1, got %d", got)
	}
	if got := atomic.LoadInt32(&upd.calls); got != 1 {
		t.Errorf("state updater calls: want 1, got %d", got)
	}
	if upd.subID != subID {
		t.Errorf("state updater sub id: want %s, got %s", subID, upd.subID)
	}
	if upd.reason != "auth_revoked" {
		t.Errorf("reason: want auth_revoked, got %q", upd.reason)
	}
}

// P2.7: a transient AuthRechecker error fails open — the deliveries
// insert proceeds, the state updater is NOT called, and a warning is
// logged (we don't assert on the log line in this integration test;
// the unit test of the recheck SPI covers fail-open).
func TestIntegrationP27_RecheckErrorFailsOpen(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	seedAuthClient(t, s, "client-failopen")
	subID, err := s.Subscriptions().Insert(ctx, s.Pool().Pgx(), repos.SubscriptionRow{
		ClientID:    "client-failopen",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/order-changed",
		ChannelType: "rest-hook",
		Endpoint:    "https://sub.example.org/notif",
		FilterBy:    filterByJSON(map[string]string{"filterParameter": "patient", "value": "Patient/123"}),
		Content:     "id-only",
	})
	if err != nil {
		t.Fatalf("insert sub: %v", err)
	}
	rcID, _ := seedEhrEvent(t, s, "http://example.org/order-changed", "ServiceRequest/abc",
		[]byte(`{"resourceType":"ServiceRequest","id":"abc","subject":{"reference":"Patient/123"},"status":"active"}`),
	)

	upd := &recordingStateUpdater{}
	w := submatcher.NewWorker(s.Pool().Pgx(), s.Subscriptions(), s.EhrEvents(), s.Deliveries(), submatcher.Config{ClaimBatchSize: 1},
		submatcher.WithAuthRechecker(errAuthRechecker{}),
		submatcher.WithStateUpdater(upd),
	)
	if _, err := w.TickOnce(ctx); err != nil {
		t.Fatalf("TickOnce: %v", err)
	}

	var n int
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT count(*) FROM deliveries WHERE ehr_event_id=$1 AND subscription_id=$2`, rcID, subID,
	).Scan(&n); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if n != 1 {
		t.Fatalf("recheck error must fail-open and produce a delivery, got %d", n)
	}
	if got := atomic.LoadInt32(&upd.calls); got != 0 {
		t.Errorf("state updater must not be called on fail-open: got %d", got)
	}
}
