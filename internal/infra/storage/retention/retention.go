// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package retention is the daily retention sweeper for non-partitioned
// tables. It runs chunked DELETEs to keep WAL pressure low.
package retention

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config controls the sweeper.
type Config struct {
	RunInterval time.Duration
	BatchSize   int32
	BatchPause  time.Duration

	Hl7MessageQueue time.Duration
	Deliveries      time.Duration
	DeadLetters     time.Duration
	AuditLog        time.Duration

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

// Tick runs one sweep cycle.
func Tick(ctx context.Context, pool *pgxpool.Pool, cfg Config) error {
	if pool == nil {
		return fmt.Errorf("retention: nil pool")
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

	if cfg.Hl7MessageQueue > 0 {
		if err := sweep(ctx, pool, "hl7_message_queue",
			"processed = true AND processed_at < $1",
			now.Add(-cfg.Hl7MessageQueue), batch, pause); err != nil {
			return err
		}
	}
	if cfg.Deliveries > 0 {
		if err := sweep(ctx, pool, "deliveries",
			"status IN ('delivered', 'dead', 'failed_permanent') AND created_at < $1",
			now.Add(-cfg.Deliveries), batch, pause); err != nil {
			return err
		}
	}
	if cfg.DeadLetters > 0 {
		if err := sweep(ctx, pool, "dead_letters",
			"created_at < $1",
			now.Add(-cfg.DeadLetters), batch, pause); err != nil {
			return err
		}
	}
	if cfg.AuditLog > 0 {
		if err := sweep(ctx, pool, "audit_log",
			"occurred_at < $1",
			now.Add(-cfg.AuditLog), batch, pause); err != nil {
			return err
		}
	}
	return nil
}

func sweep(ctx context.Context, pool *pgxpool.Pool, table, predicate string, cutoff time.Time, batch int32, pause time.Duration) error {
	for {
		// Determine the row identifier column. audit_log uses seq, the
		// rest use id.
		idCol := "id"
		if table == "audit_log" {
			idCol = "seq"
		}
		sql := fmt.Sprintf(`
			WITH victims AS (
				SELECT %s FROM %s
				WHERE %s
				LIMIT %d
			)
			DELETE FROM %s
			WHERE %s IN (SELECT %s FROM victims)`,
			idCol, table, predicate, batch, table, idCol, idCol,
		)
		tag, err := pool.Exec(ctx, sql, cutoff)
		if err != nil {
			return fmt.Errorf("retention: sweep %s: %w", table, err)
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
