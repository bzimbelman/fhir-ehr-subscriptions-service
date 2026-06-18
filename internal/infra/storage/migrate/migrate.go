// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package migrate runs SQL migrations.
//
// Migration files are embedded at build time from
// internal/infra/storage/migrate/migrations/. Each file is named
// NNNN_short_description.sql. Files are loaded in lexicographic
// (= numeric) order. Versions are recorded in the schema_migrations
// table with a SHA-256 checksum so a divergent applied migration
// fails startup loudly.
package migrate

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migration is one embedded SQL migration file.
type Migration struct {
	// Version is the zero-padded numeric prefix (e.g. "0001").
	Version string
	// Filename is the basename (e.g. "0001_init.sql").
	Filename string
	// Body is the raw SQL.
	Body string
	// Checksum is the SHA-256 of Body, hex-encoded.
	Checksum string
	// Concurrent indicates the migration uses CREATE INDEX CONCURRENTLY
	// or otherwise cannot be wrapped in a transaction.
	Concurrent bool
}

// Report records what MigrateUp did.
type Report struct {
	Applied []string
	Skipped []string
}

var versionRE = regexp.MustCompile(`^(\d+)_.*\.sql$`)

// ParseVersion returns the numeric prefix of a migration filename.
func ParseVersion(filename string) (string, error) {
	m := versionRE.FindStringSubmatch(filename)
	if m == nil {
		return "", fmt.Errorf("migrate: filename %q does not match NNNN_*.sql", filename)
	}
	return m[1], nil
}

// Embedded returns the embedded migrations sorted by version ascending.
func Embedded() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("migrate: read embedded dir: %w", err)
	}

	out := make([]Migration, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		ver, err := ParseVersion(name)
		if err != nil {
			return nil, err
		}
		body, err := fs.ReadFile(migrationFS, "migrations/"+name)
		if err != nil {
			return nil, fmt.Errorf("migrate: read %s: %w", name, err)
		}
		sum := sha256.Sum256(body)
		out = append(out, Migration{
			Version:    ver,
			Filename:   name,
			Body:       string(body),
			Checksum:   hex.EncodeToString(sum[:]),
			Concurrent: detectConcurrent(string(body)),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

func detectConcurrent(body string) bool {
	return strings.Contains(strings.ToUpper(body), "CREATE INDEX CONCURRENTLY") ||
		strings.Contains(strings.ToUpper(body), "DROP INDEX CONCURRENTLY")
}

// ErrChecksumMismatch is returned when an applied migration's checksum no
// longer matches the embedded body. Continuing in that case is unsafe.
var ErrChecksumMismatch = errors.New("migrate: checksum drift between embedded and applied migration")

// Up ensures the schema_migrations table exists, then applies any
// embedded migration whose version is not present. The schema_migrations
// table is the one defined in 0001_init.sql; this function tolerates the
// case where 0001 itself creates the table (it will be a no-op insert).
//
// LLD §5 refers to this as `migrate_up`; in Go we name it without
// the package prefix to avoid stuttering (revive: exported).
func Up(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return errors.New("migrate: nil pool")
	}
	migs, err := Embedded()
	if err != nil {
		return err
	}
	return applyAll(ctx, pool, migs)
}

func applyAll(ctx context.Context, pool *pgxpool.Pool, migs []Migration) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrate: acquire: %w", err)
	}
	defer conn.Release()

	// Detect whether schema_migrations already exists. The very first
	// migration creates it as part of its body; we only create it ourselves
	// when bootstrapping a database whose first migration has already been
	// applied by an out-of-band tool.
	exists, err := schemaMigrationsExists(ctx, conn.Conn())
	if err != nil {
		return fmt.Errorf("migrate: probe schema_migrations: %w", err)
	}
	if exists {
		// Some bootstrap deployments (e.g., 0001_init.sql) created
		// schema_migrations without a checksum column. Add it if missing.
		_, _ = conn.Exec(ctx, `ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS checksum TEXT`)
	}

	// 2. Pull applied set (or treat as empty when the table doesn't yet exist).
	applied := map[string]string{}
	if exists {
		rows, err := conn.Query(ctx, `SELECT version, COALESCE(checksum, '') FROM schema_migrations`)
		if err != nil {
			return fmt.Errorf("migrate: select schema_migrations: %w", err)
		}
		for rows.Next() {
			var v, c string
			if err := rows.Scan(&v, &c); err != nil {
				rows.Close()
				return fmt.Errorf("migrate: scan: %w", err)
			}
			applied[v] = c
		}
		rows.Close()
		if rerr := rows.Err(); rerr != nil {
			return fmt.Errorf("migrate: rows: %w", rerr)
		}
	}

	for _, m := range migs {
		stored, present := applied[m.Version]
		if present {
			if stored != "" && stored != m.Checksum {
				return fmt.Errorf("%w: version=%s stored=%s embedded=%s",
					ErrChecksumMismatch, m.Version, stored, m.Checksum)
			}
			// already applied; backfill checksum if blank
			if stored == "" {
				if _, err := conn.Exec(ctx,
					`UPDATE schema_migrations SET checksum=$2 WHERE version=$1`,
					m.Version, m.Checksum); err != nil {
					return fmt.Errorf("migrate: backfill checksum %s: %w", m.Version, err)
				}
			}
			continue
		}

		if m.Concurrent {
			if _, err := conn.Exec(ctx, m.Body); err != nil {
				return fmt.Errorf("migrate: apply concurrent %s: %w", m.Version, err)
			}
			// Ensure schema_migrations has a checksum column. Bootstrap
			// migrations (e.g., 0001_init.sql) may have created the table
			// without one.
			if _, err := conn.Exec(ctx,
				`ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS checksum TEXT`); err != nil {
				return fmt.Errorf("migrate: ensure checksum column: %w", err)
			}
			if _, err := conn.Exec(ctx,
				`INSERT INTO schema_migrations(version, applied_at, checksum) VALUES ($1, now(), $2)
				 ON CONFLICT (version) DO UPDATE SET checksum = excluded.checksum`,
				m.Version, m.Checksum); err != nil {
				return fmt.Errorf("migrate: record %s: %w", m.Version, err)
			}
			continue
		}

		if err := withTx(ctx, conn.Conn(), func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, m.Body); err != nil {
				return fmt.Errorf("apply: %w", err)
			}
			// Ensure schema_migrations has a checksum column. The bootstrap
			// migration may have created the table without one; subsequent
			// migrations are safe with the IF NOT EXISTS guard.
			if _, err := tx.Exec(ctx,
				`ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS checksum TEXT`); err != nil {
				return fmt.Errorf("ensure checksum column: %w", err)
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO schema_migrations(version, applied_at, checksum) VALUES ($1, now(), $2)
				 ON CONFLICT (version) DO UPDATE SET checksum = excluded.checksum`,
				m.Version, m.Checksum); err != nil {
				return fmt.Errorf("record: %w", err)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("migrate: %s: %w", m.Version, err)
		}
	}
	return nil
}

// withTx is a small helper. Defined here to avoid a dependency on the
// outbox package (which depends on this one transitively).
func withTx(ctx context.Context, conn *pgx.Conn, fn func(pgx.Tx) error) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// schemaMigrationsExists returns true if the schema_migrations relation
// is already present in the current database.
func schemaMigrationsExists(ctx context.Context, conn *pgx.Conn) (bool, error) {
	const sql = `SELECT EXISTS(
		SELECT 1
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = 'schema_migrations'
		  AND n.nspname = ANY (current_schemas(false))
		  AND c.relkind IN ('r', 'p')
	)`
	var exists bool
	if err := conn.QueryRow(ctx, sql).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}
