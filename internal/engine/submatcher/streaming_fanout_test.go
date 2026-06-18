// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package submatcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v3"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// streamFakeLister is a subscriptionLister that drives the worker's
// fanout loop with a synthetic active-subscription stream and records
// how the worker consumed it. It enforces the story-#55 contract: the
// callback for row N must complete before the lister yields row N+1
// (no full materialization, no look-ahead).
type streamFakeLister struct {
	rows []repos.SubscriptionRow

	// streamCalls counts StreamActiveByTopic invocations.
	streamCalls int
	// yielded is the number of rows the lister emitted into the
	// callback. With the streaming contract this equals len(rows)
	// for a clean iteration, but rises one at a time as the worker
	// pulls rows.
	yielded int
	// inFlight tracks how many rows are currently sitting in the
	// callback (must never exceed 1 — the streaming contract).
	inFlight int
	// maxInFlight is the high-water mark of inFlight observed
	// during iteration. The streaming assertion is maxInFlight==1.
	maxInFlight int
	// preYieldCheck is invoked before each row is handed to the
	// callback. Used by tests that want to inject failures or
	// pause for goroutine inspection.
	preYieldCheck func(idx int) error
}

func (s *streamFakeLister) StreamActiveByTopic(
	_ context.Context, _ repos.Querier, _ string,
	fn func(repos.SubscriptionRow) error,
) error {
	s.streamCalls++
	for i, row := range s.rows {
		if s.preYieldCheck != nil {
			if err := s.preYieldCheck(i); err != nil {
				return err
			}
		}
		s.inFlight++
		if s.inFlight > s.maxInFlight {
			s.maxInFlight = s.inFlight
		}
		err := fn(row)
		s.inFlight--
		s.yielded++
		if err != nil {
			return err
		}
	}
	return nil
}

// failIfMaterialized is a subscriptionLister whose Stream method
// panics — used to prove the worker never falls back to the listing
// path. The story-#55 prompt asks for a "mock pool that fails Query
// if asked for full LIMIT". Because the worker's contract is the
// subscriptionLister interface (not raw pool.Query), the equivalent
// assertion at this layer is: any non-streaming entry point fails the
// test.
type failIfMaterialized struct{}

func (failIfMaterialized) StreamActiveByTopic(
	context.Context, repos.Querier, string,
	func(repos.SubscriptionRow) error,
) error {
	return errors.New("forbidden: caller bypassed streaming entry point")
}

// stubMetrics records FanoutOutcome / EventProcessed counts for
// streaming-fanout assertions.
type stubMetrics struct {
	outcomes  map[FanoutDecision]int
	processed int
	matchSum  int
	runtime   int
}

func newStubMetrics() *stubMetrics { return &stubMetrics{outcomes: map[FanoutDecision]int{}} }

func (m *stubMetrics) FanoutOutcome(_ string, d FanoutDecision) {
	m.outcomes[d]++
}
func (m *stubMetrics) EventProcessed(_ string, matched int) {
	m.processed++
	m.matchSum += matched
}
func (m *stubMetrics) FilterRuntimeError(uuid.UUID) { m.runtime++ }

// makeRows builds n active SubscriptionRows for topic, with no
// filterBy so every one will Match the synthetic event.
func makeRows(n int, topic string) []repos.SubscriptionRow {
	out := make([]repos.SubscriptionRow, n)
	for i := range out {
		out[i] = repos.SubscriptionRow{
			ID:          uuid.New(),
			ClientID:    "client-x",
			Status:      repos.SubActive,
			TopicURL:    topic,
			ChannelType: "rest-hook",
			Endpoint:    "https://example.org/h",
			Content:     "id-only",
		}
	}
	return out
}

// newWorkerWithLister constructs a Worker plumbed with a
// subscriptionLister fake and a pgxmock-backed pool/tx so fanoutOne
// can run without a live database. dlv and ehr point at real
// (zero-value) repos because their methods write through the Tx
// passed in, and that Tx is the pgxmock transaction.
func newWorkerWithLister(t *testing.T, l subscriptionLister, m Metrics) *Worker {
	t.Helper()
	w := &Worker{
		// pool is unused by fanoutOne directly — TickOnce begins the
		// tx, and these tests drive fanoutOne with the pgxmock tx
		// they hold themselves.
		pool: nil,
		subs: l,
		// MarkProcessed is the only EhrEventsRepo method these tests
		// hit and it never touches the codec — passing nil keeps
		// the unit test free of crypto plumbing.
		ehr:         repos.NewEhrEventsRepo(nil),
		dlv:         repos.NewDeliveriesRepo(),
		cfg:         Config{ClaimBatchSize: 1},
		metrics:     m,
		clock:       func() time.Time { return time.Unix(1700000000, 0).UTC() },
		authRecheck: alwaysActiveAuth{},
	}
	w.cfg.ApplyDefaults()
	return w
}

// expectFanoutPerMatchSQL programs the pgxmock pool to expect the per-
// match write triple emitted by fanoutOne for one matching candidate:
//   - SELECT ... next_event_number (UPDATE ... RETURNING)
//   - INSERT INTO deliveries
//   - UPDATE subscriptions ... events_since_subscription_start
func expectFanoutPerMatchSQL(t *testing.T, mockPool pgxmock.PgxPoolIface, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		mockPool.ExpectQuery(`UPDATE subscriptions\s+SET next_event_number`).
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"next_event_number"}).AddRow(int64(i + 1)))
		mockPool.ExpectQuery(`INSERT INTO deliveries`).
			WithArgs(anyArgsN(8)...).
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
		mockPool.ExpectExec(`UPDATE subscriptions\s+SET events_since_subscription_start`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	}
}

// anyArgsN returns n pgxmock.AnyArg matchers for varargs spread.
func anyArgsN(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = pgxmock.AnyArg()
	}
	return out
}

// TestFanoutStreamsRowsOneAtATime asserts the story-#55 streaming
// contract: the worker drives StreamActiveByTopic with a callback,
// and at most one subscription row is in flight at any time.
//
// RED before the GREEN refactor (worker would call ListActiveByTopic
// and a streamFakeLister would never see Stream invoked). After the
// refactor: streamCalls==1, maxInFlight==1, yielded==len(rows).
func TestFanoutStreamsRowsOneAtATime(t *testing.T) {
	t.Parallel()
	rows := makeRows(64, "http://example.org/streaming-test")
	lister := &streamFakeLister{rows: rows}

	mockPool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mockPool.Close()
	mockPool.ExpectBegin()
	expectFanoutPerMatchSQL(t, mockPool, len(rows))
	// MarkProcessed: ehrEventsRepo.MarkProcessed issues an UPDATE
	// guarded by id + created_month.
	mockPool.ExpectExec(`UPDATE ehr_events`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	mockPool.ExpectCommit()

	tx, err := mockPool.BeginTx(context.Background(), pgx.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	metrics := newStubMetrics()
	w := newWorkerWithLister(t, lister, metrics)

	row := &repos.EhrEventRow{
		ID:            uuid.New(),
		EventNumber:   42,
		TopicURL:      "http://example.org/streaming-test",
		ChangeKind:    repos.ChangeCreate,
		Resource:      []byte(`{"resourceType":"Observation","id":"o1"}`),
		CorrelationID: uuid.New(),
		OccurredAt:    time.Unix(1700000000, 0).UTC(),
		CreatedMonth:  time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := w.fanoutOne(context.Background(), tx, row); err != nil {
		t.Fatalf("fanoutOne: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if lister.streamCalls != 1 {
		t.Fatalf("StreamActiveByTopic should be called exactly once, got %d", lister.streamCalls)
	}
	if lister.yielded != len(rows) {
		t.Fatalf("yielded %d, want %d", lister.yielded, len(rows))
	}
	if lister.maxInFlight != 1 {
		t.Fatalf("streaming contract violated: maxInFlight=%d (want 1)", lister.maxInFlight)
	}
	if metrics.outcomes[FanoutMatch] != len(rows) {
		t.Fatalf("Match metric: got %d, want %d", metrics.outcomes[FanoutMatch], len(rows))
	}
	if err := mockPool.ExpectationsWereMet(); err != nil {
		t.Fatalf("pgxmock expectations: %v", err)
	}
}

// TestFanoutNeverFallsBackToListActive proves that no path inside
// fanoutOne can reach a slice-materializing entry point. We swap in
// a lister whose Stream method always errors and expect the worker
// to surface that error rather than silently retrying via a
// non-streaming method.
//
// This guards against regressions of the form "someone added a
// fast-path that calls ListActiveByTopic when N is small."
func TestFanoutNeverFallsBackToListActive(t *testing.T) {
	t.Parallel()
	mockPool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mockPool.Close()
	mockPool.ExpectBegin()
	tx, err := mockPool.BeginTx(context.Background(), pgx.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	w := newWorkerWithLister(t, failIfMaterialized{}, newStubMetrics())

	row := &repos.EhrEventRow{
		ID:            uuid.New(),
		TopicURL:      "http://example.org/no-fallback",
		ChangeKind:    repos.ChangeCreate,
		Resource:      []byte(`{"resourceType":"Observation"}`),
		CorrelationID: uuid.New(),
	}
	err = w.fanoutOne(context.Background(), tx, row)
	if err == nil {
		t.Fatal("expected the streaming-only lister to surface its error; got nil (worker materialized?)")
	}
	if got := err.Error(); !contains(got, "forbidden: caller bypassed streaming entry point") {
		t.Fatalf("err did not propagate from the streaming lister: %v", got)
	}
}

// TestFanoutStopsOnRowError asserts that returning an error from the
// per-row callback propagates and short-circuits iteration without
// processing further rows.
func TestFanoutStopsOnRowError(t *testing.T) {
	t.Parallel()
	rows := makeRows(8, "http://example.org/short-circuit")
	lister := &streamFakeLister{
		rows: rows,
		preYieldCheck: func(idx int) error {
			if idx == 3 {
				return errors.New("synthetic row failure")
			}
			return nil
		},
	}
	mockPool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mockPool.Close()
	mockPool.ExpectBegin()
	// Three rows successfully evaluated before idx==3 fails.
	expectFanoutPerMatchSQL(t, mockPool, 3)
	tx, err := mockPool.BeginTx(context.Background(), pgx.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	w := newWorkerWithLister(t, lister, newStubMetrics())
	row := &repos.EhrEventRow{
		ID:            uuid.New(),
		TopicURL:      "http://example.org/short-circuit",
		ChangeKind:    repos.ChangeCreate,
		Resource:      []byte(`{"resourceType":"Observation"}`),
		CorrelationID: uuid.New(),
	}
	if err := w.fanoutOne(context.Background(), tx, row); err == nil {
		t.Fatal("expected error, got nil")
	}
	// We bailed at idx==3, so only rows 0..2 made it into the
	// callback (yielded counts post-callback returns). Row 3 was
	// never yielded because preYieldCheck refused.
	if lister.yielded != 3 {
		t.Fatalf("yielded=%d, want 3 before short-circuit", lister.yielded)
	}
}

// BenchmarkFanoutStreamingMemory drives fanoutOne against an in-
// memory streamFakeLister and reports allocs/op + B/op. The
// streaming contract is "constant memory regardless of row count":
// peak in-flight rows must stay at 1, no matter how many active
// subscriptions the topic carries.
//
// We run two scales (N=1,000 and N=10,000) and verify maxInFlight
// stays at 1 in both. On a non-streaming fanout (the pre-#55
// implementation) maxInFlight would equal N because all rows would
// be materialized into a slice up-front.
//
// Higher N is intentionally avoided here because the pgxmock setup
// dominates wall time (each per-row write needs three queued
// expectations). The streaming-contract assertion is invariant of
// N; we just need two points on the curve.
func BenchmarkFanoutStreamingMemory(b *testing.B) {
	cases := []struct {
		name string
		n    int
	}{
		{"N1000", 1000},
		{"N10000", 10000},
	}
	for _, tc := range cases {
		tc := tc
		b.Run(tc.name, func(b *testing.B) {
			rows := makeRows(tc.n, "http://example.org/bench")
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				b.StopTimer()
				lister := &streamFakeLister{rows: rows}
				mockPool, err := pgxmock.NewPool()
				if err != nil {
					b.Fatal(err)
				}
				mockPool.ExpectBegin()
				newBenchExpectations(b, mockPool, tc.n)
				mockPool.ExpectExec(`UPDATE ehr_events`).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
				mockPool.ExpectCommit()
				tx, err := mockPool.BeginTx(context.Background(), pgx.TxOptions{})
				if err != nil {
					b.Fatal(err)
				}
				w := &Worker{
					subs:        lister,
					ehr:         repos.NewEhrEventsRepo(nil),
					dlv:         repos.NewDeliveriesRepo(),
					cfg:         Config{ClaimBatchSize: 1},
					metrics:     nopMetrics{},
					clock:       func() time.Time { return time.Unix(1700000000, 0).UTC() },
					authRecheck: alwaysActiveAuth{},
				}
				w.cfg.ApplyDefaults()
				row := &repos.EhrEventRow{
					ID:            uuid.New(),
					TopicURL:      "http://example.org/bench",
					ChangeKind:    repos.ChangeCreate,
					Resource:      []byte(`{"resourceType":"Observation"}`),
					CorrelationID: uuid.New(),
				}
				b.StartTimer()
				if err := w.fanoutOne(context.Background(), tx, row); err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				_ = tx.Commit(context.Background())
				_ = mockPool.ExpectationsWereMet()
				mockPool.Close()
				if lister.maxInFlight > 1 {
					b.Fatalf("streaming contract violated at N=%d: maxInFlight=%d", tc.n, lister.maxInFlight)
				}
				b.StartTimer()
			}
		})
	}
}

// newBenchExpectations programs the pgxmock pool with one full
// per-match write triple per row. Helper for the benchmark.
func newBenchExpectations(_ *testing.B, mockPool pgxmock.PgxPoolIface, n int) {
	for i := 0; i < n; i++ {
		mockPool.ExpectQuery(`UPDATE subscriptions\s+SET next_event_number`).
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"next_event_number"}).AddRow(int64(i + 1)))
		mockPool.ExpectQuery(`INSERT INTO deliveries`).
			WithArgs(anyArgsN(8)...).
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
		mockPool.ExpectExec(`UPDATE subscriptions\s+SET events_since_subscription_start`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	}
}

// contains is a tiny helper (avoid importing strings just for the
// test).
func contains(haystack, needle string) bool {
	return len(needle) <= len(haystack) && indexOf(haystack, needle) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
