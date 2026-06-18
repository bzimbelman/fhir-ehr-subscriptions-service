// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package outbox is the transactional-outbox helper. Every stage handoff
// in the pipeline (HL7 Message Processor → resource_changes; Topic
// Matcher → ehr_events; Subscriptions Engine → deliveries) commits the
// "mark input processed" UPDATE and the "insert output rows" INSERTs in
// the same database transaction so a crash never leaves the system in a
// half-handed-off state.
package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Tx is the subset of pgx.Tx the outbox closure needs.
//
// We keep this small so tests can inject a fake (pgxmock) and so the
// closure cannot reach behind the abstraction (e.g., to start nested
// transactions or release the underlying connection).
type Tx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Outcome reports what one outbox transaction did.
type Outcome struct {
	// RowsWritten is the sum of all RowsAffected from each Exec inside
	// the transaction.
	RowsWritten int64
	// Duration is the wall-clock time spent in the transaction.
	Duration time.Duration
	// Err is the closure's error, if any. The transaction is rolled back
	// on a non-nil Err and Run returns the same error wrapped.
	Err error
	// AlreadyProcessed is set when the closure detected that the input
	// row was already processed by another worker (the canonical race
	// outcome under SKIP LOCKED).
	AlreadyProcessed bool
}

// IsAlreadyProcessed returns true when the outbox closure detected the
// input row had already been claimed/processed by another worker.
func (o Outcome) IsAlreadyProcessed() bool { return o.AlreadyProcessed }

// PoolBeginner is the subset of *pgxpool.Pool we need. The pgxmock
// library implements this interface as well.
type PoolBeginner interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// counter is a thin wrapper that intercepts Exec calls to add up rows
// affected. Query/QueryRow pass through.
type counter struct {
	tx    pgx.Tx
	rows  int64
	count int
}

func (c *counter) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tag, err := c.tx.Exec(ctx, sql, args...)
	if err == nil {
		c.rows += tag.RowsAffected()
		c.count++
	}
	return tag, err
}

func (c *counter) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return c.tx.Query(ctx, sql, args...)
}

func (c *counter) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return c.tx.QueryRow(ctx, sql, args...)
}

// Run begins a transaction on the given pool, runs fn with a Tx that
// counts row writes, commits on nil error, rolls back on non-nil error.
func Run(ctx context.Context, pool *pgxpool.Pool, fn func(ctx context.Context, tx Tx) error) (Outcome, error) {
	if pool == nil {
		return Outcome{}, fmt.Errorf("outbox: nil pool")
	}
	return runOnBeginner(ctx, pool, fn)
}

// RunPool is the same as Run but accepts the smaller PoolBeginner
// interface. Used by tests that inject pgxmock.
func RunPool(ctx context.Context, pool PoolBeginner, fn func(ctx context.Context, tx Tx) error) (Outcome, error) {
	if pool == nil {
		return Outcome{}, fmt.Errorf("outbox: nil pool")
	}
	return runOnBeginner(ctx, pool, fn)
}

func runOnBeginner(ctx context.Context, pool PoolBeginner, fn func(ctx context.Context, tx Tx) error) (Outcome, error) {
	start := time.Now()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Outcome{Err: err, Duration: time.Since(start)}, fmt.Errorf("outbox: begin: %w", err)
	}
	c := &counter{tx: tx}
	cerr := fn(ctx, c)
	if cerr != nil {
		_ = tx.Rollback(ctx)
		return Outcome{
			RowsWritten: c.rows,
			Duration:    time.Since(start),
			Err:         cerr,
		}, fmt.Errorf("outbox: closure: %w", cerr)
	}
	if err := tx.Commit(ctx); err != nil {
		return Outcome{
			RowsWritten: c.rows,
			Duration:    time.Since(start),
			Err:         err,
		}, fmt.Errorf("outbox: commit: %w", err)
	}
	return Outcome{
		RowsWritten: c.rows,
		Duration:    time.Since(start),
	}, nil
}

// RunOnTx runs fn against an already-open transaction, counting rows
// written. Used when the caller has its own transaction lifecycle (the
// Storage.Begin path).
func RunOnTx(ctx context.Context, tx pgx.Tx, fn func(ctx context.Context, tx Tx) error) (Outcome, error) {
	start := time.Now()
	c := &counter{tx: tx}
	err := fn(ctx, c)
	return Outcome{
		RowsWritten: c.rows,
		Duration:    time.Since(start),
		Err:         err,
	}, err
}
