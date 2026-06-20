// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package migrate_test

import (
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/migrate"
)

// Schema-cleanup batch (OP #138, #139, #140, #142, #143, #144). These
// tests inspect the embedded migration bodies — they fail if the
// expected DDL is not present, which forces the implementation work
// before the file system tells us we are done.

func combinedMigrationBodyLower(t *testing.T) string {
	t.Helper()
	migs, err := migrate.Embedded()
	if err != nil {
		t.Fatalf("Embedded(): %v", err)
	}
	var b strings.Builder
	for _, m := range migs {
		b.WriteString(m.Body)
		b.WriteByte('\n')
	}
	return strings.ToLower(b.String())
}

// OP #138: dead_letters must gain a key_version column so encrypted
// payload_redacted bytes survive key rotation.
func TestMigrationsDeadLettersHasKeyVersion(t *testing.T) {
	t.Parallel()
	body := combinedMigrationBodyLower(t)
	if !strings.Contains(body, "alter table dead_letters") {
		t.Fatalf("expected an ALTER TABLE dead_letters migration; not found")
	}
	if !strings.Contains(body, "add column") || !strings.Contains(body, "key_version") {
		t.Fatalf("expected dead_letters key_version ADD COLUMN; not found in any migration")
	}
}

// OP #139: deliveries.bundle is never written; either persist it or
// drop the column. We picked drop. The migration must drop it.
func TestMigrationsDropsDeliveriesBundle(t *testing.T) {
	t.Parallel()
	body := combinedMigrationBodyLower(t)
	if !strings.Contains(body, "alter table deliveries") || !strings.Contains(body, "drop column") || !strings.Contains(body, "bundle") {
		t.Fatalf("expected ALTER TABLE deliveries DROP COLUMN bundle; not found")
	}
}

// OP #140: schema_migrations.checksum should be created by a numbered
// migration, not by an inline ALTER TABLE inside migrate.go. We assert
// both halves: the migration body adds the column AND the runner no
// longer issues the inline ALTER (covered in TestMigrateRunnerNoInlineChecksumAlter).
func TestMigrationsSchemaMigrationsChecksumIsNumbered(t *testing.T) {
	t.Parallel()
	migs, err := migrate.Embedded()
	if err != nil {
		t.Fatalf("Embedded(): %v", err)
	}
	for _, m := range migs {
		body := strings.ToLower(m.Body)
		if strings.Contains(body, "schema_migrations") &&
			strings.Contains(body, "add column") &&
			strings.Contains(body, "checksum") {
			return
		}
	}
	t.Fatalf("expected a numbered migration to ADD COLUMN checksum to schema_migrations; not found")
}

// OP #143: drop the redundant subscription_topics_url_idx index.
func TestMigrationsDropsSubscriptionTopicsURLIdx(t *testing.T) {
	t.Parallel()
	body := combinedMigrationBodyLower(t)
	if !strings.Contains(body, "drop index") || !strings.Contains(body, "subscription_topics_url_idx") {
		t.Fatalf("expected DROP INDEX subscription_topics_url_idx; not found")
	}
}

// OP #142: subscription_topics retired_at is read but never written.
// Path (b): drop retired_at and the 'retired' status check value.
func TestMigrationsDropsSubscriptionTopicsRetiredAt(t *testing.T) {
	t.Parallel()
	body := combinedMigrationBodyLower(t)
	if !strings.Contains(body, "alter table subscription_topics") ||
		!strings.Contains(body, "drop column") ||
		!strings.Contains(body, "retired_at") {
		t.Fatalf("expected subscription_topics DROP COLUMN retired_at; not found")
	}
}

// OP #144: enforce next_event_number >= events_since_subscription_start
// via a CHECK constraint added in a numbered migration. The constraint
// must be visible in some migration body that is not the original 0001.
func TestMigrationsAddsSubscriptionsEventCursorsCheck(t *testing.T) {
	t.Parallel()
	migs, err := migrate.Embedded()
	if err != nil {
		t.Fatalf("Embedded(): %v", err)
	}
	for _, m := range migs {
		if m.Version == "0001" {
			continue
		}
		body := strings.ToLower(m.Body)
		if strings.Contains(body, "add constraint") &&
			strings.Contains(body, "check") &&
			strings.Contains(body, "next_event_number") &&
			strings.Contains(body, "events_since_subscription_start") {
			return
		}
	}
	t.Fatalf("expected a numbered migration adding ADD CONSTRAINT ... CHECK relating next_event_number and events_since_subscription_start; not found")
}
