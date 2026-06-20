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
	"time"

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

// migrationAdvisoryLockID is held for the duration of applyAll so
// multi-pod rollouts cannot race the migration runner. The bigint is
// stable across releases; choose a constant that's unlikely to collide
// with any other advisory_lock the application takes (audit B-33).
const migrationAdvisoryLockID int64 = 0xFEEDFACE

// concurrentDirective is the leading-comment opt-in marker that flags
// a migration as not-transaction-wrappable. Files that need
// CREATE INDEX CONCURRENTLY (or any other statement that PostgreSQL
// refuses to run inside a transaction block) MUST start with the
// `-- @CONCURRENT` directive on the first non-blank, non-shebang line.
//
// This replaces the previous strings.Contains("CREATE INDEX CONCURRENTLY",
// ...) heuristic, which a SQL comment containing those words could trip
// (audit B-33).
const concurrentDirective = "-- @CONCURRENT"

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

// detectConcurrent returns true only when the migration's leading
// non-blank, non-comment-noise line is the explicit opt-in directive
// `-- @CONCURRENT`. We deliberately do NOT scan the whole body for
// "CREATE INDEX CONCURRENTLY" — a SQL comment that mentions those
// words would otherwise force the runner into the no-transaction path
// for a perfectly transactional migration.
//
// Migration authors who genuinely need a non-transactional run
// (e.g. CREATE INDEX CONCURRENTLY) must opt in by putting the
// directive on the first content line of the file.
func detectConcurrent(body string) bool {
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		// Allow a leading shebang-style or block-comment-open line
		// (rare, but harmless).
		if strings.HasPrefix(line, "/*") {
			continue
		}
		// The first content line is decisive: either it's the directive,
		// or this migration is transactional.
		return strings.EqualFold(line, concurrentDirective)
	}
	return false
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

	// Hold a session-level advisory lock for the whole apply pass so a
	// rolling deployment with N replicas can't race the runner against
	// itself. Two competing migrators serialize: one applies, the
	// other waits, then sees the work already done (audit B-33).
	if _, lockErr := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationAdvisoryLockID); lockErr != nil {
		return fmt.Errorf("migrate: advisory_lock: %w", lockErr)
	}
	defer func() {
		uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(uctx, `SELECT pg_advisory_unlock($1)`, migrationAdvisoryLockID)
	}()

	// Detect whether schema_migrations already exists. The very first
	// migration creates it as part of its body; we only create it ourselves
	// when bootstrapping a database whose first migration has already been
	// applied by an out-of-band tool.
	exists, err := schemaMigrationsExists(ctx, conn.Conn())
	if err != nil {
		return fmt.Errorf("migrate: probe schema_migrations: %w", err)
	}

	// Probe whether schema_migrations already has the checksum column.
	// 0001_init.sql creates the table without it; the column is added by
	// 0011_schema_migrations_checksum.sql once that migration runs. The
	// runner adapts its INSERT/SELECT shape to whichever state it finds
	// the table in, so this no longer requires an inline ALTER (OP #140).
	hasChecksum := false
	if exists {
		hasChecksum, err = schemaMigrationsHasChecksum(ctx, conn.Conn())
		if err != nil {
			return fmt.Errorf("migrate: probe schema_migrations.checksum: %w", err)
		}
	}

	// 2. Pull applied set (or treat as empty when the table doesn't yet exist).
	applied := map[string]string{}
	if exists {
		var query string
		if hasChecksum {
			query = `SELECT version, COALESCE(checksum, '') FROM schema_migrations`
		} else {
			query = `SELECT version, '' FROM schema_migrations`
		}
		var rows pgx.Rows
		rows, err = conn.Query(ctx, query)
		if err != nil {
			return fmt.Errorf("migrate: select schema_migrations: %w", err)
		}
		for rows.Next() {
			var v, c string
			if scanErr := rows.Scan(&v, &c); scanErr != nil {
				rows.Close()
				return fmt.Errorf("migrate: scan: %w", scanErr)
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
			// already applied; backfill checksum if blank (and the
			// column actually exists on this database).
			if stored == "" && hasChecksum {
				if _, uerr := conn.Exec(ctx,
					`UPDATE schema_migrations SET checksum=$2 WHERE version=$1`,
					m.Version, m.Checksum); uerr != nil {
					return fmt.Errorf("migrate: backfill checksum %s: %w", m.Version, uerr)
				}
			}
			continue
		}

		if m.Concurrent {
			if _, cerr := conn.Exec(ctx, m.Body); cerr != nil {
				return fmt.Errorf("migrate: apply concurrent %s: %w", m.Version, cerr)
			}
			// 0001 creates schema_migrations without checksum; 0011 adds
			// the column. After every apply, re-probe so the next record
			// statement matches the table's current shape.
			if !hasChecksum {
				hasChecksum, err = schemaMigrationsHasChecksum(ctx, conn.Conn())
				if err != nil {
					return fmt.Errorf("migrate: probe schema_migrations.checksum: %w", err)
				}
			}
			if err := recordApplied(ctx, conn.Conn(), m, hasChecksum); err != nil {
				return fmt.Errorf("migrate: record %s: %w", m.Version, err)
			}
			continue
		}

		if err := withTx(ctx, conn.Conn(), func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, m.Body); err != nil {
				return fmt.Errorf("apply: %w", err)
			}
			// Re-probe after applying inside the same transaction: a
			// migration body that adds the checksum column is visible to
			// the same tx, so we record with the right shape immediately.
			localHasChecksum := hasChecksum
			if !localHasChecksum {
				probed, perr := txSchemaMigrationsHasChecksum(ctx, tx)
				if perr != nil {
					return fmt.Errorf("probe schema_migrations.checksum: %w", perr)
				}
				localHasChecksum = probed
			}
			if err := recordAppliedTx(ctx, tx, m, localHasChecksum); err != nil {
				return fmt.Errorf("record: %w", err)
			}
			// If this tx flipped the column-exists state, persist that
			// for subsequent iterations.
			hasChecksum = localHasChecksum
			return nil
		}); err != nil {
			return fmt.Errorf("migrate: %s: %w", m.Version, err)
		}
	}
	return nil
}

// recordApplied inserts (or upserts) an applied-row outside any tx,
// branching on whether the schema_migrations table has a checksum
// column at this point in the apply pass.
func recordApplied(ctx context.Context, conn *pgx.Conn, m Migration, hasChecksum bool) error {
	if hasChecksum {
		_, err := conn.Exec(ctx,
			`INSERT INTO schema_migrations(version, applied_at, checksum) VALUES ($1, now(), $2)
			 ON CONFLICT (version) DO UPDATE SET checksum = excluded.checksum`,
			m.Version, m.Checksum)
		return err
	}
	_, err := conn.Exec(ctx,
		`INSERT INTO schema_migrations(version, applied_at) VALUES ($1, now())
		 ON CONFLICT (version) DO NOTHING`, m.Version)
	return err
}

// recordAppliedTx is recordApplied but bound to a transaction.
func recordAppliedTx(ctx context.Context, tx pgx.Tx, m Migration, hasChecksum bool) error {
	if hasChecksum {
		_, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations(version, applied_at, checksum) VALUES ($1, now(), $2)
			 ON CONFLICT (version) DO UPDATE SET checksum = excluded.checksum`,
			m.Version, m.Checksum)
		return err
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations(version, applied_at) VALUES ($1, now())
		 ON CONFLICT (version) DO NOTHING`, m.Version)
	return err
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

// schemaMigrationsHasChecksum returns true if schema_migrations has a
// `checksum` column. 0001_init.sql creates the table without it; 0011
// adds it. The runner adapts its INSERT shape to whichever state it
// finds (OP #140).
func schemaMigrationsHasChecksum(ctx context.Context, conn *pgx.Conn) (bool, error) {
	const sql = `SELECT EXISTS (
		SELECT 1
		FROM information_schema.columns
		WHERE table_name = 'schema_migrations'
		  AND column_name = 'checksum'
		  AND table_schema = ANY (current_schemas(false))
	)`
	var has bool
	if err := conn.QueryRow(ctx, sql).Scan(&has); err != nil {
		return false, err
	}
	return has, nil
}

// txSchemaMigrationsHasChecksum is schemaMigrationsHasChecksum bound to
// a transaction so a migration body that adds the column can record
// itself with the right shape inside the same tx.
func txSchemaMigrationsHasChecksum(ctx context.Context, tx pgx.Tx) (bool, error) {
	const sql = `SELECT EXISTS (
		SELECT 1
		FROM information_schema.columns
		WHERE table_name = 'schema_migrations'
		  AND column_name = 'checksum'
		  AND table_schema = ANY (current_schemas(false))
	)`
	var has bool
	if err := tx.QueryRow(ctx, sql).Scan(&has); err != nil {
		return false, err
	}
	return has, nil
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
