// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package repos_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v3"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/storage/repos"
)

func TestDeadLettersInsert(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	id := uuid.New()
	srcID := uuid.New()

	pool.ExpectQuery("INSERT INTO dead_letters").
		WithArgs(anyArgs(8)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(id))

	repo := repos.NewDeadLettersRepo(newCodec(t))
	got, err := repo.Insert(context.Background(), pool, repos.DeadLetterRow{
		Kind:            "hl7_unparseable",
		SourceTable:     "hl7_message_queue",
		SourceID:        srcID,
		Reason:          "bad msh",
		PayloadRedacted: []byte(`{"redacted":true}`),
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if got != id {
		t.Errorf("expected %v got %v", id, got)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestPendingPairsInsertAndDelete(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	srcID := uuid.New()
	pool.ExpectExec("INSERT INTO pending_pairs").
		WithArgs(anyArgs(6)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectExec("DELETE FROM pending_pairs").
		WithArgs(anyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	repo := repos.NewPendingPairsRepo(newCodec(t))
	if err := repo.Insert(ctx, pool, repos.PendingPairRow{
		CorrelationKey:   "ORD-123",
		ListenerEndpoint: "orm",
		PendingResource:  []byte(`{"resourceType":"ServiceRequest"}`),
		PendingKind:      repos.PendingDelete,
		SourceMessageID:  srcID,
		ExpiresAt:        time.Now().Add(30 * time.Second),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := repo.Delete(ctx, pool, "ORD-123", "orm"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestAdapterStateUpsertAndGet(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	pool.ExpectExec("INSERT INTO adapter_state").
		WithArgs(anyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectQuery("SELECT (.+) FROM adapter_state").
		WithArgs(anyArgs(3)...).
		WillReturnRows(pgxmock.NewRows([]string{"value", "key_version", "updated_at"}).
			AddRow([]byte("vendor-token"), int32(1), time.Now()))

	repo := repos.NewAdapterStateRepo()
	if err := repo.Upsert(ctx, pool, repos.AdapterStateRow{
		AdapterID: "epic", Scope: "vendor_token", Key: "interconnect",
		Value:      []byte("vendor-token"),
		KeyVersion: 1,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := repo.Get(ctx, pool, "epic", "vendor_token", "interconnect")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected row")
	}
	if string(got.Value) != "vendor-token" {
		t.Errorf("got %q", got.Value)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestAuthClientsInsertAndGet(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	pool.ExpectExec("INSERT INTO auth_clients").
		WithArgs(anyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectQuery("SELECT (.+) FROM auth_clients").
		WithArgs("client-a").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "jwks_url", "scopes", "display_name", "created_at", "updated_at",
		}).AddRow("client-a", "https://idp/jwks", []string{"system/Subscription.cruds"},
			"Lab", time.Now(), time.Now()))

	repo := repos.NewAuthClientsRepo()
	if err := repo.Insert(ctx, pool, repos.AuthClientRow{
		ID:          "client-a",
		JwksURL:     "https://idp/jwks",
		Scopes:      []string{"system/Subscription.cruds"},
		DisplayName: "Lab",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := repo.GetByID(ctx, pool, "client-a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil || got.ID != "client-a" {
		t.Fatalf("expected client-a got %+v", got)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestSubscriptionTopicsInsertAndList(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	id := uuid.New()
	pool.ExpectQuery("INSERT INTO subscription_topics").
		WithArgs(anyArgs(9)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(id))

	now := time.Now()
	pool.ExpectQuery("SELECT (.+) FROM subscription_topics WHERE status").
		WithArgs("active").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "url", "version", "title", "description", "status",
			"date", "source", "body", "compiled_form", "created_at", "retired_at",
		}).AddRow(id, "http://example.org/order-changed", "1.0", "Order Changed",
			"desc", "active", &now, "builtin",
			[]byte(`{"resourceType":"SubscriptionTopic"}`),
			[]byte(nil), now, (*time.Time)(nil)))

	repo := repos.NewSubscriptionTopicsRepo()
	got, err := repo.Insert(ctx, pool, repos.SubscriptionTopicRow{
		URL:     "http://example.org/order-changed",
		Version: "1.0",
		Title:   "Order Changed",
		Status:  "active",
		Source:  "builtin",
		Body:    []byte(`{"resourceType":"SubscriptionTopic"}`),
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if got != id {
		t.Errorf("got id %v want %v", got, id)
	}
	rows, err := repo.ListByStatus(ctx, pool, "active")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row got %d", len(rows))
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestSubscriptionsInsertAndGet(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	id := uuid.New()
	pool.ExpectQuery("INSERT INTO subscriptions").
		WithArgs(anyArgs(17)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(id))
	pool.ExpectQuery("SELECT (.+) FROM subscriptions WHERE id").
		WithArgs(id).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "client_id", "status", "topic_url", "channel_type", "endpoint",
			"header", "filter_by", "content", "heartbeat_period", "timeout",
			"max_count", "events_since_subscription_start", "reason", "end_time",
			"error", "contact", "last_handshake_at", "created_at", "updated_at",
		}).AddRow(id, "client-a", "requested", "http://example.org/topic", "rest-hook",
			"https://sub", []byte(nil), []byte(nil), "id-only", nil, nil,
			int32(1), int64(0), "", nil, "", []byte(nil), nil, time.Now(), time.Now()))

	repo := repos.NewSubscriptionsRepo()
	got, err := repo.Insert(ctx, pool, repos.SubscriptionRow{
		ClientID:    "client-a",
		Status:      repos.SubRequested,
		TopicURL:    "http://example.org/topic",
		ChannelType: "rest-hook",
		Endpoint:    "https://sub",
		Content:     "id-only",
		MaxCount:    1,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if got != id {
		t.Errorf("got id %v want %v", got, id)
	}
	row, err := repo.GetByID(ctx, pool, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row == nil || row.ID != id {
		t.Fatalf("got %+v", row)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestWsBindingTokensInsertAndDelete(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	subID := uuid.New()
	pool.ExpectExec("INSERT INTO ws_binding_tokens").
		WithArgs(anyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectExec("DELETE FROM ws_binding_tokens").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	repo := repos.NewWsBindingTokensRepo()
	if err := repo.Insert(ctx, pool, repos.WsBindingTokenRow{
		Token:          "tok",
		SubscriptionID: subID,
		ClientID:       "client-a",
		ExpiresAt:      time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := repo.Delete(ctx, pool, "tok"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestAuditLogAppend(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	pool.ExpectQuery("INSERT INTO audit_log").
		WithArgs(anyArgs(10)...).
		WillReturnRows(pgxmock.NewRows([]string{"seq"}).AddRow(int64(42)))

	repo := repos.NewAuditLogRepo()
	got, err := repo.Append(ctx, pool, repos.AuditLogRow{
		ActorKind:     "system",
		Action:        "create_subscription",
		Outcome:       "success",
		CanonicalForm: []byte(`{"a":1}`),
		Hash:          []byte("hash"),
		PrevHash:      []byte("prev"),
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if got != 42 {
		t.Errorf("expected seq=42, got %d", got)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}
