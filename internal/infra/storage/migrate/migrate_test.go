// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package migrate_test

import (
	"context"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/migrate"
)

func TestMigrationsEmbedded(t *testing.T) {
	t.Parallel()

	migs, err := migrate.Embedded()
	if err != nil {
		t.Fatalf("Embedded() error: %v", err)
	}
	if len(migs) == 0 {
		t.Fatal("expected at least one embedded migration")
	}
	if migs[0].Version != "0001" {
		t.Fatalf("expected first migration version '0001', got %q", migs[0].Version)
	}
	if len(migs[0].Body) == 0 {
		t.Fatal("expected migration body to be non-empty")
	}
	if migs[0].Checksum == "" {
		t.Fatal("expected migration checksum to be populated")
	}
}

func TestMigrationsAreSortedByVersion(t *testing.T) {
	t.Parallel()

	migs, err := migrate.Embedded()
	if err != nil {
		t.Fatalf("Embedded() error: %v", err)
	}
	for i := 1; i < len(migs); i++ {
		if migs[i-1].Version >= migs[i].Version {
			t.Fatalf("migrations not sorted: %s >= %s", migs[i-1].Version, migs[i].Version)
		}
	}
}

func TestParseVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		filename string
		want     string
		wantErr  bool
	}{
		{"0001_init.sql", "0001", false},
		{"0017_add_index.sql", "0017", false},
		{"bad_no_prefix.sql", "", true},
		{"00ab_garbage.sql", "", true},
	}
	for _, tc := range tests {
		got, err := migrate.ParseVersion(tc.filename)
		if (err != nil) != tc.wantErr {
			t.Errorf("ParseVersion(%q) err=%v wantErr=%v", tc.filename, err, tc.wantErr)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseVersion(%q) = %q, want %q", tc.filename, got, tc.want)
		}
	}
}

func TestUpRequiresPool(t *testing.T) {
	t.Parallel()

	// Up with a nil pool must fail loudly, not panic.
	err := migrate.Up(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error from nil pool")
	}
}
