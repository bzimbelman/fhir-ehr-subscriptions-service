// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package submatcher

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v3"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// scopedFakeLister is a subscriptionLister that records the clientID
// argument passed by the worker. Pre-#232 the worker invoked
// StreamActiveByTopic with topic only, so cross-tenant subscriptions
// shared a topic-stream and a single ehr_events row could fan out to
// every tenant's subscriptions on the same topic. Post-#232 the
// worker MUST pass row.ClientID so the stream returns only the
// recipient's subscriptions.
type scopedFakeLister struct {
	rows           []repos.SubscriptionRow
	gotClientID    string
	gotTopicURL    string
	streamCalls    int
	yieldedClients []string
}

func (s *scopedFakeLister) StreamActiveByTopic(
	_ context.Context, _ repos.Querier, topicURL string, clientID string,
	fn func(repos.SubscriptionRow) error,
) error {
	s.streamCalls++
	s.gotTopicURL = topicURL
	s.gotClientID = clientID
	for _, row := range s.rows {
		// The lister is the contract boundary: it must filter by
		// clientID before yielding (mirrors the SQL predicate the
		// repo will add). Tests assert both that the worker passed
		// the right id AND that the lister returned only rows for
		// that id.
		if clientID != "" && row.ClientID != clientID {
			continue
		}
		s.yieldedClients = append(s.yieldedClients, row.ClientID)
		if err := fn(row); err != nil {
			return err
		}
	}
	return nil
}

// OP #232: fanoutOne must scope the subscription stream to the event
// row's ClientID. Two clients (A, B) own active subscriptions on the
// same topic. An ehr_events row addressed to client A must NOT cause
// a delivery insert against client B's subscription — that would be
// cross-tenant secret disclosure (subscriber endpoint, headers,
// filterBy criteria are all per-tenant secrets).
func TestFanoutOne_ScopesStreamByClientID(t *testing.T) {
	t.Parallel()

	topicURL := "http://example.org/topics/orders"
	subA := repos.SubscriptionRow{
		ID:          uuid.New(),
		ClientID:    "client-a",
		Status:      repos.SubActive,
		TopicURL:    topicURL,
		ChannelType: "rest-hook",
		Endpoint:    "https://a.example.org/wh",
		Content:     "id-only",
	}
	subB := repos.SubscriptionRow{
		ID:          uuid.New(),
		ClientID:    "client-b",
		Status:      repos.SubActive,
		TopicURL:    topicURL,
		ChannelType: "rest-hook",
		Endpoint:    "https://b.example.org/wh",
		Content:     "id-only",
	}
	lister := &scopedFakeLister{rows: []repos.SubscriptionRow{subA, subB}}

	mockPool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mockPool.Close()
	mockPool.ExpectBegin()
	// Only ONE subscription should match (client-a's). One per-match
	// SQL pair, not two.
	mockPool.ExpectQuery(`UPDATE subscriptions\s+SET next_event_number`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"next_event_number"}).AddRow(int64(1)))
	mockPool.ExpectQuery(`INSERT INTO deliveries`).
		WithArgs(anyArgsN(8)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	mockPool.ExpectExec(`UPDATE subscriptions\s+SET events_since_subscription_start.*unnest`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	mockPool.ExpectExec(`UPDATE ehr_events`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	mockPool.ExpectCommit()

	tx, err := mockPool.BeginTx(context.Background(), pgx.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	w := newWorkerWithLister(t, lister, newStubMetrics())
	row := &repos.EhrEventRow{
		ID:            uuid.New(),
		EventNumber:   42,
		ClientID:      "client-a", // recipient is client-a only
		TopicURL:      topicURL,
		ChangeKind:    repos.ChangeCreate,
		Resource:      []byte(`{"resourceType":"Observation","id":"o1"}`),
		CorrelationID: uuid.New(),
		OccurredAt:    time.Unix(1700000000, 0).UTC(),
		CreatedMonth:  time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := w.fanoutOne(context.Background(), tx, row); err != nil {
		t.Fatalf("fanoutOne: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if lister.streamCalls != 1 {
		t.Fatalf("StreamActiveByTopic should be called once; got %d", lister.streamCalls)
	}
	if lister.gotClientID != "client-a" {
		t.Fatalf("worker passed clientID=%q; want %q (event recipient)", lister.gotClientID, "client-a")
	}
	if lister.gotTopicURL != topicURL {
		t.Errorf("topicURL=%q; want %q", lister.gotTopicURL, topicURL)
	}
	if len(lister.yieldedClients) != 1 || lister.yieldedClients[0] != "client-a" {
		t.Errorf("yieldedClients=%v; want exactly [client-a]", lister.yieldedClients)
	}

	if err := mockPool.ExpectationsWereMet(); err != nil {
		t.Errorf("pgxmock expectations: %v", err)
	}
}
