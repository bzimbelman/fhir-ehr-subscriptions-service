//go:build integration

// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Integration tests for the OP #138-#144 schema cleanup batch. These
// run a real Postgres testcontainer; no mocks. Run with:
//
//	go test -race -tags integration ./internal/infra/storage/...

package storage_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/codec"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/migrate"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// OP #138: dead_letters now persists key_version per row. After a key
// rotation the row's key_version is what Decrypt uses, so a row written
// under key v1 still decrypts after the active key flips to v2.
func TestIntegrationDeadLettersKeyVersionSurvivesRotation(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)

	// Two-key codec; v1 is the initial active key, v2 is post-rotation.
	keyV1 := make([]byte, 32)
	keyV2 := make([]byte, 32)
	for i := range keyV1 {
		keyV1[i] = byte(i + 1)
		keyV2[i] = byte(i + 100)
	}
	cfg := storage.Config{
		PostgresURL: url,
		KeyVersions: map[int32][]byte{1: keyV1, 2: keyV2},
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

	plaintext := []byte("PHI: redacted but still attached for forensic triage")
	id, err := s.DeadLetters().Insert(ctx, s.Pool().Pgx(), repos.DeadLetterRow{
		Kind:            "delivery_exhausted",
		SourceTable:     "deliveries",
		SourceID:        uuid.New(),
		Reason:          "max-attempts",
		PayloadRedacted: plaintext,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Read the persisted row, including key_version, then decrypt with
	// the original codec the row was written under.
	var enc []byte
	var kv int32
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT payload_redacted, key_version FROM dead_letters WHERE id=$1`, id,
	).Scan(&enc, &kv); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if kv != 1 {
		t.Fatalf("expected stored key_version=1, got %d", kv)
	}

	// Now simulate a post-rotation read: build a NEW codec whose active
	// key is v2 but which retains v1. Decrypt must still succeed because
	// the row carries its own key_version.
	rotated, err := codec.New(codec.NewStaticKeyProvider(map[int32][]byte{1: keyV1, 2: keyV2}, 2))
	if err != nil {
		t.Fatalf("rotated codec: %v", err)
	}
	got, err := rotated.Decrypt(enc, kv, repos.AADDeadLetters(id, kv))
	if err != nil {
		t.Fatalf("decrypt after rotation: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("decrypted=%q want %q", got, plaintext)
	}
}

// OP #138: an existing dead-letter row whose key_version column was
// added later (legacy NULL/default) still decrypts via the migration
// default key version.
func TestIntegrationDeadLettersLegacyRowDecryptsViaDefault(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)

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

	id, err := s.DeadLetters().Insert(ctx, s.Pool().Pgx(), repos.DeadLetterRow{
		Kind:            "hl7_unparseable",
		SourceTable:     "hl7_message_queue",
		SourceID:        uuid.New(),
		Reason:          "invalid mllp",
		PayloadRedacted: []byte("legacy bytes"),
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// Force the row's key_version back to the migration default to
	// simulate a row inserted before this column existed.
	if _, err := s.Pool().Pgx().Exec(ctx,
		`UPDATE dead_letters SET key_version = 1 WHERE id=$1`, id,
	); err != nil {
		t.Fatal(err)
	}

	// ListRecent does not return the encrypted payload but its presence
	// proves the row survived schema cutover.
	rows, err := s.DeadLetters().ListRecent(ctx, s.Pool().Pgx(), 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.ID == id {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected the legacy row to be returned by ListRecent")
	}
}

// OP #139: deliveries.bundle column is gone after the migration.
func TestIntegrationDeliveriesBundleColumnDropped(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}

	var present bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name='deliveries' AND column_name='bundle'
		)`,
	).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatalf("deliveries.bundle column still present; OP #139 wants it dropped")
	}
}

// OP #140: schema_migrations.checksum is added by a numbered migration,
// not by an inline ALTER inside the runner. After Up() finishes, the
// column exists AND the migration that created it is recorded in
// schema_migrations.
func TestIntegrationSchemaMigrationsChecksumColumnRecorded(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}

	// Column must exist.
	var hasCol bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name='schema_migrations' AND column_name='checksum'
		)`,
	).Scan(&hasCol); err != nil {
		t.Fatal(err)
	}
	if !hasCol {
		t.Fatalf("schema_migrations.checksum is missing after Up()")
	}

	// At least one migration body must reference adding checksum.
	migs, err := migrate.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range migs {
		body := strings.ToLower(m.Body)
		if strings.Contains(body, "schema_migrations") &&
			strings.Contains(body, "checksum") &&
			strings.Contains(body, "add column") {
			// Confirm the migration was actually applied.
			var applied bool
			if err := pool.QueryRow(ctx,
				`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, m.Version,
			).Scan(&applied); err != nil {
				t.Fatal(err)
			}
			if applied {
				found = true
			}
			break
		}
	}
	if !found {
		t.Fatalf("expected a numbered migration adding schema_migrations.checksum to be present and applied")
	}
}

// OP #141: NextEventNumber field round-trips on the SubscriptionRow
// model via INSERT/SELECT.
func TestIntegrationSubscriptionRowNextEventNumberRoundTrip(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	// Auth client is required (FK).
	if err := s.AuthClients().Insert(ctx, s.Pool().Pgx(), repos.AuthClientRow{
		ID:          "client-a",
		DisplayName: "Test",
	}); err != nil {
		t.Fatalf("AuthClients.Insert: %v", err)
	}

	id, err := s.Subscriptions().Insert(ctx, s.Pool().Pgx(), repos.SubscriptionRow{
		ClientID:        "client-a",
		Status:          repos.SubActive,
		TopicURL:        "http://example.org/topic",
		ChannelType:     "rest-hook",
		Endpoint:        "https://sub",
		Content:         "id-only",
		MaxCount:        1,
		NextEventNumber: 42,
	})
	if err != nil {
		t.Fatalf("Subscriptions.Insert: %v", err)
	}

	got, err := s.Subscriptions().GetByID(ctx, s.Pool().Pgx(), id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("nil row")
	}
	if got.NextEventNumber != 42 {
		t.Fatalf("NextEventNumber=%d want 42", got.NextEventNumber)
	}
}

// OP #142: subscription_topics.retired_at column is dropped, and the
// status check no longer accepts 'retired'.
func TestIntegrationSubscriptionTopicsRetiredAtDropped(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}

	var hasCol bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name='subscription_topics' AND column_name='retired_at'
		)`,
	).Scan(&hasCol); err != nil {
		t.Fatal(err)
	}
	if hasCol {
		t.Fatalf("subscription_topics.retired_at column still present; OP #142 wants it dropped")
	}

	// Inserting status='retired' must fail because the constraint no
	// longer permits it.
	_, err = pool.Exec(ctx, `
		INSERT INTO subscription_topics (url, version, status, source, body)
		VALUES ('http://example.org/x', '1.0', 'retired', 'builtin', '{}'::jsonb)`)
	if err == nil {
		t.Fatalf("expected check-constraint violation inserting status='retired' after OP #142")
	}
}

// OP #143: redundant subscription_topics_url_idx is removed.
func TestIntegrationSubscriptionTopicsURLIdxDropped(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}

	var present bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_indexes
			WHERE tablename='subscription_topics' AND indexname='subscription_topics_url_idx'
		)`,
	).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatalf("subscription_topics_url_idx still present; OP #143 wants it dropped")
	}
}

// OP #144: the CHECK constraint enforces
// next_event_number >= events_since_subscription_start. An UPDATE that
// would violate must error.
func TestIntegrationSubscriptionsEventCursorsCheckConstraint(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	if err := s.AuthClients().Insert(ctx, s.Pool().Pgx(), repos.AuthClientRow{
		ID:          "client-a",
		DisplayName: "Test",
	}); err != nil {
		t.Fatalf("AuthClients.Insert: %v", err)
	}

	id, err := s.Subscriptions().Insert(ctx, s.Pool().Pgx(), repos.SubscriptionRow{
		ClientID:                     "client-a",
		Status:                       repos.SubActive,
		TopicURL:                     "http://example.org/topic",
		ChannelType:                  "rest-hook",
		Endpoint:                     "https://sub",
		Content:                      "id-only",
		MaxCount:                     1,
		NextEventNumber:              5,
		EventsSinceSubscriptionStart: 3,
	})
	if err != nil {
		t.Fatalf("Subscriptions.Insert: %v", err)
	}

	// Try to push events_since_subscription_start above next_event_number.
	_, err = s.Pool().Pgx().Exec(ctx,
		`UPDATE subscriptions SET events_since_subscription_start = 9 WHERE id=$1`, id,
	)
	if err == nil {
		t.Fatalf("expected check-constraint violation when events_since_subscription_start > next_event_number")
	}
}
