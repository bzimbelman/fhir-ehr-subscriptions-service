// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package repos_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v3"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// hashTokenForTest mirrors the unexported hashing convention used by
// WsBindingTokensRepo: sha256 of the cleartext, hex-encoded.
func hashTokenForTest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// P1.12: every successful dead-letter Insert fires the registered
// reporter exactly once with the row's Kind. Wiring uses this to bump
// a fhir_subs_dead_letters_total{reason} counter. The reporter is a
// global function pointer so this test cannot run concurrently with
// other dead_letters Inserts in the same package — left non-Parallel.
func TestDeadLettersReporterFires(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	id := uuid.New()
	srcID := uuid.New()
	pool.ExpectQuery("INSERT INTO dead_letters").
		WithArgs(anyArgs(9)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(id))

	var (
		mu          sync.Mutex
		calls       int
		seenReason  string
		seenForKind = "delivery_exhausted"
	)
	repos.SetDeadLetterReporter(func(reason string) {
		mu.Lock()
		defer mu.Unlock()
		// Only count the call for the unique kind we issued from this
		// test. Other parallel repo tests insert different kinds; the
		// global reporter sees them too, which is fine — we just don't
		// count them.
		if reason == seenForKind {
			calls++
			seenReason = reason
		}
	})
	t.Cleanup(func() { repos.SetDeadLetterReporter(nil) })

	repo := repos.NewDeadLettersRepo(newCodec(t))
	if _, err := repo.Insert(context.Background(), pool, repos.DeadLetterRow{
		Kind:        seenForKind,
		SourceTable: "deliveries",
		SourceID:    srcID,
		Reason:      "max attempts",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("reporter call count for %q: want 1, got %d", seenForKind, calls)
	}
	if seenReason != seenForKind {
		t.Errorf("reporter reason: want %q, got %q", seenForKind, seenReason)
	}
}

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
		WithArgs(anyArgs(9)...).
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
		WithArgs(anyArgs(7)...).
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
	if upErr := repo.Upsert(ctx, pool, repos.AdapterStateRow{
		AdapterID: "epic", Scope: "vendor_token", Key: "interconnect",
		Value:      []byte("vendor-token"),
		KeyVersion: 1,
	}); upErr != nil {
		t.Fatalf("upsert: %v", upErr)
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
	if insErr := repo.Insert(ctx, pool, repos.AuthClientRow{
		ID:          "client-a",
		JwksURL:     "https://idp/jwks",
		Scopes:      []string{"system/Subscription.cruds"},
		DisplayName: "Lab",
	}); insErr != nil {
		t.Fatalf("insert: %v", insErr)
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
		WithArgs("active", repos.DefaultListByStatusCap, 0).
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

func TestWsBindingTokensInsert(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	subID := uuid.New()
	expires := time.Now().Add(time.Minute)
	cleartext := "tok"
	hashed := hashTokenForTest(cleartext)

	// Insert must hash internally — the SQL receives sha256(cleartext)
	// while the caller still passes cleartext.
	pool.ExpectExec("INSERT INTO ws_binding_tokens").
		WithArgs(hashed, subID, "client-a", expires).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	repo := repos.NewWsBindingTokensRepo()
	if err := repo.Insert(ctx, pool, repos.WsBindingTokenRow{
		Token:          cleartext,
		SubscriptionID: subID,
		ClientID:       "client-a",
		ExpiresAt:      expires,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestWsBindingTokensConsume drives the four documented Consume outcomes
// against pgxmock. The TOCTOU fix in 0002_ws_binding_tokens_consumed.sql
// rides on the SQL — a unit-level assertion that each branch maps to the
// right ConsumeOutcome value (without requiring Docker) keeps the
// invariant covered on every CI run.
func TestWsBindingTokensConsume(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	repo := repos.NewWsBindingTokensRepo()

	t.Run("ok", func(t *testing.T) {
		t.Parallel()
		pool, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()

		subID := uuid.New()
		// Caller passes cleartext; repo must hash before the WHERE match.
		pool.ExpectQuery("UPDATE ws_binding_tokens").
			WithArgs(hashTokenForTest("tok-ok"), now).
			WillReturnRows(pgxmock.NewRows([]string{"subscription_id", "client_id"}).
				AddRow(subID, "client-a"))

		got, err := repo.Consume(context.Background(), pool, "tok-ok", now)
		if err != nil {
			t.Fatalf("consume: %v", err)
		}
		if got.Outcome != repos.ConsumeOK {
			t.Errorf("outcome = %v, want ConsumeOK", got.Outcome)
		}
		if got.SubscriptionID != subID {
			t.Errorf("subID = %v, want %v", got.SubscriptionID, subID)
		}
		if got.ClientID != "client-a" {
			t.Errorf("clientID = %q", got.ClientID)
		}
		if err := pool.ExpectationsWereMet(); err != nil {
			t.Errorf("expectations: %v", err)
		}
	})

	t.Run("not_found", func(t *testing.T) {
		t.Parallel()
		pool, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()

		// UPDATE returns 0 rows -> pgx.ErrNoRows on Scan.
		pool.ExpectQuery("UPDATE ws_binding_tokens").
			WithArgs(hashTokenForTest("tok-missing"), now).
			WillReturnRows(pgxmock.NewRows([]string{"subscription_id", "client_id"}))
		// Diagnostic SELECT also returns 0 rows.
		pool.ExpectQuery("SELECT consumed_at IS NOT NULL").
			WithArgs(hashTokenForTest("tok-missing"), now).
			WillReturnRows(pgxmock.NewRows([]string{"consumed", "expired"}))

		got, err := repo.Consume(context.Background(), pool, "tok-missing", now)
		if err != nil {
			t.Fatalf("consume: %v", err)
		}
		if got.Outcome != repos.ConsumeNotFound {
			t.Errorf("outcome = %v, want ConsumeNotFound", got.Outcome)
		}
		if err := pool.ExpectationsWereMet(); err != nil {
			t.Errorf("expectations: %v", err)
		}
	})

	t.Run("already_used", func(t *testing.T) {
		t.Parallel()
		pool, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()

		pool.ExpectQuery("UPDATE ws_binding_tokens").
			WithArgs(hashTokenForTest("tok-used"), now).
			WillReturnRows(pgxmock.NewRows([]string{"subscription_id", "client_id"}))
		// Diagnostic: consumed_at IS NOT NULL -> consumed=true.
		pool.ExpectQuery("SELECT consumed_at IS NOT NULL").
			WithArgs(hashTokenForTest("tok-used"), now).
			WillReturnRows(pgxmock.NewRows([]string{"consumed", "expired"}).
				AddRow(true, false))

		got, err := repo.Consume(context.Background(), pool, "tok-used", now)
		if err != nil {
			t.Fatalf("consume: %v", err)
		}
		if got.Outcome != repos.ConsumeAlreadyUsed {
			t.Errorf("outcome = %v, want ConsumeAlreadyUsed", got.Outcome)
		}
		if err := pool.ExpectationsWereMet(); err != nil {
			t.Errorf("expectations: %v", err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		t.Parallel()
		pool, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()

		pool.ExpectQuery("UPDATE ws_binding_tokens").
			WithArgs(hashTokenForTest("tok-stale"), now).
			WillReturnRows(pgxmock.NewRows([]string{"subscription_id", "client_id"}))
		// Diagnostic: expires_at <= now -> expired=true.
		pool.ExpectQuery("SELECT consumed_at IS NOT NULL").
			WithArgs(hashTokenForTest("tok-stale"), now).
			WillReturnRows(pgxmock.NewRows([]string{"consumed", "expired"}).
				AddRow(false, true))

		got, err := repo.Consume(context.Background(), pool, "tok-stale", now)
		if err != nil {
			t.Fatalf("consume: %v", err)
		}
		if got.Outcome != repos.ConsumeExpired {
			t.Errorf("outcome = %v, want ConsumeExpired", got.Outcome)
		}
		if err := pool.ExpectationsWereMet(); err != nil {
			t.Errorf("expectations: %v", err)
		}
	})
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

// TestAuditLogAppendChainedOK exercises the happy path of the
// defense-in-depth chain check (S-13 #2): the prior row's hash is
// fetched and compared to the caller-supplied PrevHash before insert.
func TestAuditLogAppendChainedOK(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	pool.ExpectQuery("SELECT chain_hash FROM audit_log").
		WillReturnRows(pgxmock.NewRows([]string{"hash"}).AddRow([]byte("prev")))
	pool.ExpectQuery("INSERT INTO audit_log").
		WithArgs(anyArgs(10)...).
		WillReturnRows(pgxmock.NewRows([]string{"seq"}).AddRow(int64(43)))

	repo := repos.NewAuditLogRepo()
	got, err := repo.AppendChained(ctx, pool, repos.AuditLogRow{
		ActorKind:     "system",
		Action:        "create_subscription",
		Outcome:       "success",
		CanonicalForm: []byte(`{"a":1}`),
		Hash:          []byte("hash"),
		PrevHash:      []byte("prev"),
	})
	if err != nil {
		t.Fatalf("append-chained: %v", err)
	}
	if got != 43 {
		t.Errorf("expected seq=43, got %d", got)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestAuditLogAppendChainedMismatch verifies that a wrong PrevHash
// returns ErrAuditPrevHashMismatch and does NOT issue an INSERT.
func TestAuditLogAppendChainedMismatch(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	pool.ExpectQuery("SELECT chain_hash FROM audit_log").
		WillReturnRows(pgxmock.NewRows([]string{"hash"}).AddRow([]byte("real-prev")))
	// Note: NO ExpectQuery for INSERT — pgxmock fails the test if it ever runs.

	repo := repos.NewAuditLogRepo()
	_, err = repo.AppendChained(ctx, pool, repos.AuditLogRow{
		ActorKind:     "system",
		Action:        "create_subscription",
		Outcome:       "success",
		CanonicalForm: []byte(`{"a":1}`),
		Hash:          []byte("hash"),
		PrevHash:      []byte("forged-prev"),
	})
	if !errors.Is(err, repos.ErrAuditPrevHashMismatch) {
		t.Fatalf("expected ErrAuditPrevHashMismatch, got %v", err)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestAuditLogAppendChainedGenesis verifies that the first-ever row
// (audit_log empty) requires PrevHash to be empty.
func TestAuditLogAppendChainedGenesis(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	pool.ExpectQuery("SELECT chain_hash FROM audit_log").
		WillReturnRows(pgxmock.NewRows([]string{"hash"}))
	pool.ExpectQuery("INSERT INTO audit_log").
		WithArgs(anyArgs(10)...).
		WillReturnRows(pgxmock.NewRows([]string{"seq"}).AddRow(int64(1)))

	repo := repos.NewAuditLogRepo()
	got, err := repo.AppendChained(ctx, pool, repos.AuditLogRow{
		ActorKind:     "system",
		Action:        "boot",
		Outcome:       "success",
		CanonicalForm: []byte(`{"genesis":true}`),
		Hash:          []byte("first-hash"),
		PrevHash:      nil,
	})
	if err != nil {
		t.Fatalf("genesis append: %v", err)
	}
	if got != 1 {
		t.Errorf("expected seq=1, got %d", got)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestAuditLogAppendChainedGenesisMismatch verifies that a non-empty
// PrevHash is rejected when the table is empty.
func TestAuditLogAppendChainedGenesisMismatch(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	pool.ExpectQuery("SELECT chain_hash FROM audit_log").
		WillReturnRows(pgxmock.NewRows([]string{"hash"}))

	repo := repos.NewAuditLogRepo()
	_, err = repo.AppendChained(ctx, pool, repos.AuditLogRow{
		ActorKind: "system", Action: "boot", Outcome: "success",
		CanonicalForm: []byte(`{}`), Hash: []byte("h"), PrevHash: []byte("forged"),
	})
	if !errors.Is(err, repos.ErrAuditPrevHashMismatch) {
		t.Fatalf("expected ErrAuditPrevHashMismatch, got %v", err)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}
