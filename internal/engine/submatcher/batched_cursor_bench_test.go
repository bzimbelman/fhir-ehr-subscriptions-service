// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package submatcher

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// latencyTx is a minimal pgx.Tx that returns success for Exec and
// pretends each call costs a fixed amount of wall time. The fanout
// transaction's bottleneck on a hot topic is round-trip latency to
// the database, not local Go work, so a per-Exec sleep is the
// honest model — comparing N tiny round-trips to ceil(N/cap) of
// them is exactly what story #56 is about.
//
// We only fill in the Exec method (the rest panics) because the
// benchmark exercises batchedAdvanceCursor and a hand-rolled
// per-row equivalent — neither needs Begin/Commit/QueryRow/etc.
type latencyTx struct {
	perCallLatency time.Duration
	execCalls      int
}

func (t *latencyTx) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	t.execCalls++
	if t.perCallLatency > 0 {
		time.Sleep(t.perCallLatency)
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

// All other pgx.Tx methods are unused by these benchmarks; keep them
// as panics so a future caller that strays here gets a loud failure
// instead of a silently-wrong result.
func (t *latencyTx) Begin(context.Context) (pgx.Tx, error) {
	panic("latencyTx.Begin not implemented")
}
func (t *latencyTx) BeginFunc(context.Context, func(pgx.Tx) error) error {
	panic("latencyTx.BeginFunc not implemented")
}
func (t *latencyTx) Commit(context.Context) error   { panic("latencyTx.Commit not implemented") }
func (t *latencyTx) Rollback(context.Context) error { panic("latencyTx.Rollback not implemented") }
func (t *latencyTx) CopyFrom(
	context.Context, pgx.Identifier, []string, pgx.CopyFromSource,
) (int64, error) {
	panic("latencyTx.CopyFrom not implemented")
}
func (t *latencyTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults {
	panic("latencyTx.SendBatch not implemented")
}
func (t *latencyTx) LargeObjects() pgx.LargeObjects {
	panic("latencyTx.LargeObjects not implemented")
}
func (t *latencyTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	panic("latencyTx.Prepare not implemented")
}
func (t *latencyTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("latencyTx.Query not implemented")
}
func (t *latencyTx) QueryRow(context.Context, string, ...any) pgx.Row {
	panic("latencyTx.QueryRow not implemented")
}
func (t *latencyTx) Conn() *pgx.Conn { panic("latencyTx.Conn not implemented") }

// BenchmarkCursorAdvance_PerRow_vs_Batched compares the pre-#56
// inline-per-match UPDATE pattern against the batched
// `unnest($1::uuid[], $2::bigint[])` form.
//
// The benchmark uses a fake pgx.Tx with a fixed per-Exec latency to
// simulate Postgres wire round-trips (a real fanout pays one round-trip
// per Exec; a Go-only test would otherwise hide the headline win).
//
// Acceptance criterion (story #56): >= 10x throughput improvement on
// a 1000-subscriber topic. With perCallLatency = 100µs (a generous
// in-DC round-trip estimate), the per-row path issues 1000 Exec
// calls and the batched path issues 1; the batched form should run
// ~1000x faster in this micro-benchmark, comfortably above 10x.
func BenchmarkCursorAdvance_PerRow_vs_Batched(b *testing.B) {
	const N = 1000
	// 100µs per Exec is conservative for a same-VPC RDS round trip
	// (real numbers tend to be 200-500µs). Keep it small enough to
	// run the bench in a few seconds at b.N >= 10.
	const perCallLatency = 100 * time.Microsecond

	ctx := context.Background()
	ids := make([]uuid.UUID, N)
	nums := make([]int64, N)
	for i := range ids {
		ids[i] = uuid.New()
		nums[i] = int64(i + 1)
	}

	b.Run("PerRow_N1000", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			tx := &latencyTx{perCallLatency: perCallLatency}
			for j := 0; j < N; j++ {
				if _, err := tx.Exec(ctx,
					`UPDATE subscriptions SET events_since_subscription_start = GREATEST(events_since_subscription_start, $1), updated_at = now() WHERE id = $2`,
					nums[j], ids[j]); err != nil {
					b.Fatal(err)
				}
			}
			if tx.execCalls != N {
				b.Fatalf("PerRow: execCalls=%d, want %d", tx.execCalls, N)
			}
		}
	})

	b.Run("Batched_N1000", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			tx := &latencyTx{perCallLatency: perCallLatency}
			if err := batchedAdvanceCursor(ctx, tx, ids, nums); err != nil {
				b.Fatal(err)
			}
			if tx.execCalls != 1 {
				b.Fatalf("Batched: execCalls=%d, want 1", tx.execCalls)
			}
		}
	})
}
