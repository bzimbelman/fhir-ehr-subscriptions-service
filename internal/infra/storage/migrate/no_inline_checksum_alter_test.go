// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package migrate_test

import (
	"os"
	"strings"
	"testing"
)

// OP #140: the ALTER that adds schema_migrations.checksum belongs in a
// numbered migration, not in the runner. This test reads migrate.go
// directly and fails if the inline ALTER is still there.
func TestMigrateRunnerNoInlineChecksumAlter(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("migrate.go")
	if err != nil {
		t.Fatalf("read migrate.go: %v", err)
	}
	body := strings.ToLower(string(src))
	needle := "alter table schema_migrations add column if not exists checksum"
	if strings.Contains(body, needle) {
		t.Fatalf("migrate.go still issues an inline ALTER TABLE schema_migrations ADD COLUMN checksum; OP #140 wants this moved into a numbered migration")
	}
}
