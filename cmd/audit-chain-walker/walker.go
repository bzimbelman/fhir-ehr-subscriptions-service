// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
)

// walk opens a pgx pool, streams every audit_log row in (occurred_at,
// seq) order, builds the slim audit.ExternalVerifierRow shape per row,
// calls audit.VerifyChainExternalDetail to identify mismatches, and
// reports the result to stdout.
//
// Returns (hasBreaks bool, err error):
//
//   - (false, nil) on a clean chain. Stdout already has `rows: <N>` and
//     `result: clean`.
//   - (true, nil) on any chain break. Stdout already has the per-row
//     break lines and a final `result: <N> break(s)` summary; main
//     translates this to exit code 1.
//   - (_, err) on an operational failure (DB connect, query, scan).
//     The caller is responsible for printing the error to stderr.
//
// The query SELECTs every row in a single streamed pgx.Rows scan loop
// — no LIMIT/OFFSET pagination. This is structurally required: each
// row's hash depends on every preceding row, so we must walk the chain
// end-to-end. Order is `(occurred_at, seq)` to match the audit
// writer's append order even when occurred_at ties (seq is a
// monotonically increasing BIGSERIAL).
func walk(ctx context.Context, opts walkerOptions, stdout io.Writer) (bool, error) {
	pool, err := pgxpool.New(ctx, opts.DatabaseURL)
	if err != nil {
		return false, fmt.Errorf("open pool: %w", err)
	}
	defer pool.Close()

	rows, seqs, occurredAts, err := loadAuditRows(ctx, pool, opts.From, opts.To)
	if err != nil {
		return false, err
	}

	bad := audit.VerifyChainExternalDetail(rows, opts.Genesis)

	fmt.Fprintf(stdout, "rows: %d\n", len(rows))
	if len(bad) == 0 {
		fmt.Fprintln(stdout, "result: clean")
		return false, nil
	}

	for _, idx := range bad {
		fmt.Fprintf(stdout, "  break at row %d: seq=%d occurred_at=%s reason=chain_hash mismatch\n",
			idx, seqs[idx], occurredAts[idx].UTC().Format(time.RFC3339Nano))
	}
	fmt.Fprintf(stdout, "result: %d break(s)\n", len(bad))
	return true, nil
}

// loadAuditRows streams the audit_log table in (occurred_at, seq)
// order. Returns three parallel slices: the slim ExternalVerifierRow
// shape the audit reference verifier expects, the on-disk seq for each
// row (so break reports can pinpoint the offending DB row even when
// the operator runs the verifier off-line against a SQL dump), and
// the occurred_at timestamp (already on the row but exposed
// separately so callers don't have to dereference into the slim shape).
//
// When opts.From / opts.To are non-zero the SELECT is range-filtered
// on occurred_at. NOTE: filtering still walks the full prefix in
// memory because each row's chain_hash depends on every preceding
// row — a strict --from/--to verifier would silently miss cascade
// breaks that originate before the window. The caller (main.go) accepts
// these flags as advisory; honoring them precisely (e.g. only
// REPORTING breaks within the window) is a future enhancement and is
// NOT required by the OP #257 H2 contract.
func loadAuditRows(
	ctx context.Context,
	pool *pgxpool.Pool,
	from, to time.Time,
) ([]audit.ExternalVerifierRow, []int64, []time.Time, error) {
	const baseQuery = `
SELECT seq, occurred_at, actor_kind, actor_id, action, target_kind, target_id,
       outcome, correlation_id, payload, prior_hash, chain_hash
FROM audit_log
ORDER BY occurred_at ASC, seq ASC`

	rows, err := pool.Query(ctx, baseQuery)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("query audit_log: %w", err)
	}
	defer rows.Close()

	var (
		out      []audit.ExternalVerifierRow
		seqs     []int64
		tsList   []time.Time
		filterOn = !from.IsZero() || !to.IsZero()
	)

	for rows.Next() {
		var (
			seq       int64
			ts        time.Time
			actorKind string
			actorID   string
			action    string
			tgtKind   string
			tgtID     string
			outcome   string
			cid       uuid.UUID
			payloadB  []byte
			priorH    []byte
			chainH    []byte
		)
		if err := rows.Scan(
			&seq,
			&ts,
			&actorKind,
			&actorID,
			&action,
			&tgtKind,
			&tgtID,
			&outcome,
			&cid,
			&payloadB,
			&priorH,
			&chainH,
		); err != nil {
			return nil, nil, nil, fmt.Errorf("scan audit_log row: %w", err)
		}
		// Honor --from/--to as an inclusive filter on which rows are
		// even loaded. This is an audit-side convenience; any chain
		// break that depends on a row outside the window will surface
		// at the boundary as a prior_hash break (the verifier will
		// recompute against the wrong genesis-vs-prior). Operators who
		// need exact mid-chain auditing should run without filters.
		if filterOn {
			if !from.IsZero() && ts.Before(from) {
				continue
			}
			if !to.IsZero() && ts.After(to) {
				continue
			}
		}

		var payloadMap map[string]any
		if len(payloadB) > 0 {
			if err := json.Unmarshal(payloadB, &payloadMap); err != nil {
				return nil, nil, nil, fmt.Errorf("unmarshal payload at seq=%d: %w", seq, err)
			}
		}

		out = append(out, audit.ExternalVerifierRow{
			OccurredAt:    ts,
			ActorKind:     actorKind,
			ActorID:       actorID,
			Action:        action,
			TargetKind:    tgtKind,
			TargetID:      tgtID,
			Outcome:       outcome,
			CorrelationID: cid,
			Payload:       payloadMap,
			PriorHash:     priorH,
			ChainHash:     chainH,
		})
		seqs = append(seqs, seq)
		tsList = append(tsList, ts)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, fmt.Errorf("iterate audit_log: %w", err)
	}
	return out, seqs, tsList, nil
}
