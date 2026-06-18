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

// hasSkipLocked reports whether the SQL string carries both `FOR UPDATE`
// and `SKIP LOCKED` clauses outside of comments. SQL comments and string
// literals are stripped first so a payload like
// `-- FOR UPDATE SKIP LOCKED` cannot trick the runtime check (N-1).
func hasSkipLocked(sql string) bool {
	stripped := stripSQLCommentsAndStrings(sql)
	upper := strings.ToUpper(stripped)
	return strings.Contains(upper, "FOR UPDATE") && strings.Contains(upper, "SKIP LOCKED")
}

// stripSQLCommentsAndStrings removes `--` line comments, `/* ... */`
// block comments, and the contents of single-quoted string literals
// from s. It is intentionally lightweight (no SQL parser dependency)
// and only needs to be robust against the patterns ClaimUnprocessed
// callers compose at compile time.
func stripSQLCommentsAndStrings(s string) string {
	var out strings.Builder
	i := 0
	n := len(s)
	for i < n {
		c := s[i]
		// Line comment: -- to end of line.
		if c == '-' && i+1 < n && s[i+1] == '-' {
			for i < n && s[i] != '\n' {
				i++
			}
			continue
		}
		// Block comment: /* ... */
		if c == '/' && i+1 < n && s[i+1] == '*' {
			i += 2
			for i+1 < n && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			if i+1 < n {
				i += 2
			} else {
				i = n
			}
			continue
		}
		// Single-quoted string literal: '...' with '' as escaped quote.
		if c == '\'' {
			i++
			for i < n {
				if s[i] == '\'' {
					if i+1 < n && s[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			continue
		}
		out.WriteByte(c)
		i++
	}
	return out.String()
}
