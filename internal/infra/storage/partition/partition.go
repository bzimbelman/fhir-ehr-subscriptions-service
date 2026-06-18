// Copyright the fhir-subscriptions-foss authors.
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

	// Now is overridable for tests.
	Now func() time.Time
}

// Run is a long-lived loop. It calls Tick once at startup, then sleeps
// RunInterval between ticks. Returns when ctx is canceled.
func Run(ctx context.Context, pool *pgxpool.Pool, cfg Config) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RunInterval == 0 {
		cfg.RunInterval = 24 * time.Hour
	}
	// Best-effort first tick.
	_ = Tick(ctx, pool, cfg)

	t := time.NewTimer(cfg.RunInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = Tick(ctx, pool, cfg)
			t.Reset(cfg.RunInterval)
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
		detach := fmt.Sprintf(`ALTER TABLE %s DETACH PARTITION %s`, table, p)
		if _, err := pool.Exec(ctx, detach); err != nil {
			return fmt.Errorf("partition: detach %s: %w", p, err)
		}
		drop := fmt.Sprintf(`DROP TABLE %s`, p)
		if _, err := pool.Exec(ctx, drop); err != nil {
			return fmt.Errorf("partition: drop %s: %w", p, err)
		}
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
