// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package partition is the daily partition maintainer for the two
// month-partitioned tables (resource_changes, ehr_events).
//
// The maintainer always runs one month ahead so a write at the boundary
// never finds a missing partition. Idempotent: CREATE TABLE IF NOT
// EXISTS makes concurrent runs safe.
package partition

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config controls the maintainer.
type Config struct {
	RunInterval              time.Duration
	LockTimeout              time.Duration
	AutoDrop                 bool
	ResourceChangesRetention time.Duration
	EhrEventsRetention       time.Duration

	// TickTimeout caps how long a single Run-driven Tick may take.
	// Default: 30m. Without a per-Tick deadline, a stuck DETACH/DROP
	// could pin the maintainer goroutine forever even with a healthy
	// shutdown ctx.
	TickTimeout time.Duration

	// OnTickError is invoked once per Tick if the cycle returned a
	// non-nil error. Tick errors used to be dropped on the floor; the
	// hook lets the host log/observe them. Optional.
	OnTickError func(error)

	// Now is overridable for tests.
	Now func() time.Time
}

// Run is a long-lived loop. It calls Tick once at startup, then sleeps
// for the most recent RunInterval before each subsequent tick. Returns
// when ctx is canceled.
//
// N-1: prior versions captured cfg.RunInterval once and ignored later
// reloads. Run now re-reads cfg.RunInterval (and cfg.TickTimeout) on
// every iteration, so a SIGHUP-driven config reload that mutates the
// supplied cfg pointer takes effect on the next tick. Callers passing
// Config by value (the common case) see no behavior change.
func Run(ctx context.Context, pool *pgxpool.Pool, cfg Config) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RunInterval == 0 {
		cfg.RunInterval = 24 * time.Hour
	}
	if cfg.TickTimeout <= 0 {
		cfg.TickTimeout = 30 * time.Minute
	}
	tickOnce := func() {
		timeout := cfg.TickTimeout
		if timeout <= 0 {
			timeout = 30 * time.Minute
		}
		tickCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := Tick(tickCtx, pool, cfg); err != nil && cfg.OnTickError != nil {
			cfg.OnTickError(err)
		}
	}
	tickOnce()

	for {
		interval := cfg.RunInterval
		if interval <= 0 {
			interval = 24 * time.Hour
		}
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
			tickOnce()
		}
	}
}

// Tick runs one cycle of partition maintenance.
func Tick(ctx context.Context, pool *pgxpool.Pool, cfg Config) error {
	if pool == nil {
		return fmt.Errorf("partition: nil pool")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	now := cfg.Now()
	for _, t := range []struct {
		Table     string
		Retention time.Duration
	}{
		{"resource_changes", cfg.ResourceChangesRetention},
		{"ehr_events", cfg.EhrEventsRetention},
	} {
		if err := createNextMonth(ctx, pool, t.Table, now); err != nil {
			return err
		}
		if cfg.AutoDrop && t.Retention > 0 {
			if err := dropOlderThan(ctx, pool, t.Table, now.Add(-t.Retention)); err != nil {
				return err
			}
		}
	}
	return nil
}

func firstOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func createNextMonth(ctx context.Context, pool *pgxpool.Pool, table string, now time.Time) error {
	thisMonth := firstOfMonth(now)
	// We aim "next month" specifically.
	next := thisMonth.AddDate(0, 1, 0)
	end := next.AddDate(0, 1, 0)
	suffix := next.Format("2006_01")
	ddl := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s_%s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')`,
		table, suffix, table, next.Format("2006-01-02"), end.Format("2006-01-02"),
	)
	_, err := pool.Exec(ctx, ddl)
	if err != nil {
		return fmt.Errorf("partition: create %s_%s: %w", table, suffix, err)
	}
	return nil
}

func dropOlderThan(ctx context.Context, pool *pgxpool.Pool, table string, cutoff time.Time) error {
	// Find partitions whose end date is <= cutoff.
	const findSQL = `
		SELECT inhrelid::regclass::text
		FROM pg_inherits
		WHERE inhparent = $1::regclass`
	rows, err := pool.Query(ctx, findSQL, table)
	if err != nil {
		return fmt.Errorf("partition: enumerate %s: %w", table, err)
	}
	defer rows.Close()
	candidates := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("partition: scan: %w", err)
		}
		candidates = append(candidates, name)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("partition: rows: %w", err)
	}
	cutoffMonth := firstOfMonth(cutoff)
	for _, p := range candidates {
		// Convention: partitions are named <parent>_YYYY_MM.
		// Anything we cannot parse is left alone.
		ts, ok := parseSuffixDate(p, table)
		if !ok || !ts.Before(cutoffMonth) {
			continue
		}
		if err := dropOnePartition(ctx, pool, table, p); err != nil {
			return err
		}
	}
	return nil
}

// dropOnePartition runs DETACH + DROP in a single transaction so a
// crash between the two steps cannot leave an orphan partition that
// is no longer attached to its parent but still owns its data.
func dropOnePartition(ctx context.Context, pool *pgxpool.Pool, parent, child string) (retErr error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("partition: begin %s: %w", child, err)
	}
	defer func() {
		if retErr != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	detach := fmt.Sprintf(`ALTER TABLE %s DETACH PARTITION %s`, parent, child)
	if _, err := tx.Exec(ctx, detach); err != nil {
		return fmt.Errorf("partition: detach %s: %w", child, err)
	}
	drop := fmt.Sprintf(`DROP TABLE %s`, child)
	if _, err := tx.Exec(ctx, drop); err != nil {
		return fmt.Errorf("partition: drop %s: %w", child, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("partition: commit drop %s: %w", child, err)
	}
	return nil
}

func parseSuffixDate(partition, parent string) (time.Time, bool) {
	prefix := parent + "_"
	if len(partition) < len(prefix)+7 || partition[:len(prefix)] != prefix {
		return time.Time{}, false
	}
	tail := partition[len(prefix):]
	t, err := time.Parse("2006_01", tail)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
