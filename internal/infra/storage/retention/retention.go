// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package retention is the daily retention sweeper for non-partitioned
// tables. It runs chunked DELETEs to keep WAL pressure low.
//
// The sweeper deliberately excludes audit_log: the audit chain is
// hash-linked, so any DELETE breaks chain validation. Audit retention
// is handled by partition rotation (out-of-scope for this package;
// tracked separately).
package retention

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// retentionAdvisoryLockID is the constant we feed to pg_advisory_lock
// for the duration of a sweep. Any pod that races for the lock blocks
// until the holder finishes; multi-pod deployments therefore can't
// stomp on each other's chunked DELETEs (audit B-32).
//
// The value is arbitrary but stable; the bigint is split into the
// (classid, objid) pair pg_advisory_lock(int4, int4) uses internally.
const retentionAdvisoryLockID int64 = 0x52455445_4E54494F // 'RETENTIO'

// SweepExecTimeout is the per-statement deadline we wrap every Exec
// in so a single chunk cannot hang the connection indefinitely. 30s is
// enough for very large chunks (default batch is 5000 rows) without
// pinning a connection through a network partition.
const SweepExecTimeout = 30 * time.Second

// sweepTarget is a whitelisted (table, idCol) pair plus its predicate
// template. New tables MUST be added explicitly here — the predicate
// is parameterized but the table/column are concatenated into the SQL
// after passing through this struct, so only known-safe values can
// reach the database (audit B-32 SQL injection vector).
type sweepTarget struct {
	Table     string
	IDCol     string
	Predicate string
}

// allowedTargets enumerates the only tables retention is allowed to
// sweep. audit_log is intentionally absent: hash-chain integrity does
// not survive a DELETE, so audit retention is partition-rotation only.
var allowedTargets = map[string]sweepTarget{
	"hl7_message_queue": {
		Table: "hl7_message_queue", IDCol: "id",
		Predicate: "processed = true AND processed_at < $1",
	},
	"deliveries": {
		// Use the schema's actual status enum: 'failed' and 'dead' (not
		// 'failed_permanent', which the schema does not define).
		Table: "deliveries", IDCol: "id",
		Predicate: "status IN ('delivered', 'dead', 'failed') AND created_at < $1",
	},
	"dead_letters": {
		Table: "dead_letters", IDCol: "id",
		Predicate: "created_at < $1",
	},
}

// Config controls the sweeper.
type Config struct {
	RunInterval time.Duration
	BatchSize   int32
	BatchPause  time.Duration

	// TickTimeout caps how long a single Run-driven Tick may take. A
	// stuck advisory lock or a misbehaving DELETE can no longer wedge
	// the sweeper for the lifetime of the process. Default: 6h (one
	// quarter of the default RunInterval).
	TickTimeout time.Duration

	Hl7MessageQueue time.Duration
	Deliveries      time.Duration
	DeadLetters     time.Duration
	// AuditLog is accepted for backwards compatibility but is no
	// longer honored: audit retention is handled by partition rotation,
	// not row-by-row DELETE, because the audit table is hash-chained
	// (audit B-32). A non-zero value is silently ignored.
	AuditLog time.Duration

	// Now is overridable for tests.
	Now func() time.Time
}

// Run is a long-lived loop. Returns when ctx is canceled.
func Run(ctx context.Context, pool *pgxpool.Pool, cfg Config) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RunInterval == 0 {
		cfg.RunInterval = 24 * time.Hour
	}
	if cfg.TickTimeout <= 0 {
		cfg.TickTimeout = 6 * time.Hour
	}
	tickOnce := func() {
		tickCtx, cancel := context.WithTimeout(ctx, cfg.TickTimeout)
		defer cancel()
		_ = Tick(tickCtx, pool, cfg)
	}
	tickOnce()
	t := time.NewTimer(cfg.RunInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tickOnce()
			t.Reset(cfg.RunInterval)
		}
	}
}

// Tick runs one sweep cycle. The whole tick runs under a session-level
// pg_advisory_lock so multi-pod deployments don't stomp on each other.
func Tick(ctx context.Context, pool *pgxpool.Pool, cfg Config) error {
	if pool == nil {
		return errors.New("retention: nil pool")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	now := cfg.Now()
	batch := cfg.BatchSize
	if batch == 0 {
		batch = 5000
	}
	pause := cfg.BatchPause
	if pause == 0 {
		pause = 100 * time.Millisecond
	}

	// Acquire the advisory lock for the whole sweep on a dedicated
	// connection so tries from other pods serialize.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("retention: acquire conn: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, retentionAdvisoryLockID); err != nil {
		return fmt.Errorf("retention: advisory_lock: %w", err)
	}
	defer func() {
		// Best-effort unlock; if the connection is being released the
		// lock is auto-released anyway.
		uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(uctx, `SELECT pg_advisory_unlock($1)`, retentionAdvisoryLockID)
	}()

	if cfg.Hl7MessageQueue > 0 {
		if err := sweep(ctx, conn.Conn(), allowedTargets["hl7_message_queue"],
			now.Add(-cfg.Hl7MessageQueue), batch, pause); err != nil {
			return err
		}
	}
	if cfg.Deliveries > 0 {
		if err := sweep(ctx, conn.Conn(), allowedTargets["deliveries"],
			now.Add(-cfg.Deliveries), batch, pause); err != nil {
			return err
		}
	}
	if cfg.DeadLetters > 0 {
		if err := sweep(ctx, conn.Conn(), allowedTargets["dead_letters"],
			now.Add(-cfg.DeadLetters), batch, pause); err != nil {
			return err
		}
	}
	// cfg.AuditLog is intentionally ignored; see Config.AuditLog.
	return nil
}

// sweep runs chunked DELETEs against the target table. The table and
// column come from the whitelisted sweepTarget; only the cutoff
// timestamp is parameterized into the SQL.
func sweep(
	ctx context.Context,
	conn *pgx.Conn,
	target sweepTarget,
	cutoff time.Time,
	batch int32,
	pause time.Duration,
) error {
	// Defense-in-depth: refuse to run if a caller mutated the target.
	if _, ok := allowedTargets[target.Table]; !ok {
		return fmt.Errorf("retention: refusing to sweep non-whitelisted table %q", target.Table)
	}

	// Build SQL once. ORDER BY <idCol> guarantees deterministic claim
	// order so two retention runs (e.g., one operator-driven, one
	// scheduled) cannot starve each other in lock contention. The
	// `batch` value is rendered as a literal integer because it is
	// statically typed and bounded; the cutoff is parameterized.
	sql := fmt.Sprintf(`
		WITH victims AS (
			SELECT %s FROM %s
			WHERE %s
			ORDER BY %s
			LIMIT %d
		)
		DELETE FROM %s
		WHERE %s IN (SELECT %s FROM victims)`,
		target.IDCol, target.Table, target.Predicate, target.IDCol,
		batch, target.Table, target.IDCol, target.IDCol,
	)

	for {
		execCtx, cancel := context.WithTimeout(ctx, SweepExecTimeout)
		tag, err := conn.Exec(execCtx, sql, cutoff)
		cancel()
		if err != nil {
			return fmt.Errorf("retention: sweep %s: %w", target.Table, err)
		}
		n := tag.RowsAffected()
		if n < int64(batch) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pause):
		}
	}
}
