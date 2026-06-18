// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package claim implements the SELECT FOR UPDATE SKIP LOCKED claim
// primitive that every queue-style stage uses to pull rows out of its
// input table without blocking on rows another worker is already
// processing.
//
// The primitive is intentionally generic. Each repository wraps it with
// table-specific SQL and decode logic.
package claim

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Scanner is the row-level decoder. Implementations call this to pull
// columns into typed fields.
type Scanner func(dest ...any) error

// Unprocessed runs the given SQL inside the caller-provided
// transaction, decoding each returned row via decode. The SQL must
// include "FOR UPDATE SKIP LOCKED" — without it, a stuck worker would
// block other workers, which defeats the purpose. We reject the SQL at
// runtime if the clause is missing rather than silently producing the
// wrong concurrency story.
//
// The caller commits the transaction. The lock is held for the
// transaction's lifetime; the caller is expected to UPDATE the rows
// they claimed (typically marking processed) before commit.
//
// LLD §6 calls the primitive `ClaimUnprocessed`; in Go we name it
// without the package prefix to avoid stuttering (revive: exported).
func Unprocessed[T any](
	ctx context.Context,
	tx pgx.Tx,
	decode func(Scanner) (T, error),
	sql string,
	args ...any,
) ([]T, error) {
	if tx == nil {
		return nil, errors.New("claim: nil tx")
	}
	if !hasSkipLocked(sql) {
		return nil, errors.New("claim: SQL must include FOR UPDATE SKIP LOCKED")
	}

	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("claim: query: %w", err)
	}
	defer rows.Close()

	out := make([]T, 0, 8)
	for rows.Next() {
		v, err := decode(Scanner(rows.Scan))
		if err != nil {
			return nil, fmt.Errorf("claim: decode: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim: rows iteration: %w", err)
	}
	return out, nil
}

func hasSkipLocked(sql string) bool {
	upper := strings.ToUpper(sql)
	return strings.Contains(upper, "FOR UPDATE") && strings.Contains(upper, "SKIP LOCKED")
}
