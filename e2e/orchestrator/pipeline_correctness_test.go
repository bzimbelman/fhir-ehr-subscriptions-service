// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	hpipe "github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/harness"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/resthook"
)

// TestScenario_Pipeline_FanoutTo50Subscribers verifies that 50
// subscribers on the same topic each get exactly one delivery for one
// inbound HL7 message, and that each subscription's event_number is
// monotonic from 1 (i.e., the submatcher's per-subscription cursor is
// advanced exactly once).
//
// Why this matters: a regression in the submatcher's "claim N events,
// fan out, RETURNING-update each subscription's cursor" flow could
// either (a) drop deliveries for some subscribers or (b) increment the
// same subscription's cursor twice. Either silently breaks SLA. 50
// subscribers is enough to exercise the claim-batch boundary without
// being flaky.
func TestScenario_Pipeline_FanoutTo50Subscribers(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := newDeadline(context.Background(), 120*time.Second)
	defer cancel()

	tlsSrv, err := hpipe.StartTLSRestHookServer(h.MockSub.RestHook.Handler())
	if err != nil {
		t.Fatalf("tls rest-hook: %v", err)
	}
	t.Cleanup(func() { _ = tlsSrv.Close() })
	restCh, err := resthook.New(resthook.Options{HTTPClient: tlsSrv.Client()})
	if err != nil {
		t.Fatalf("resthook.New: %v", err)
	}

	fx := newScenarioFixture(t, ctx, h, scenarioConfig{
		preBuiltTLS: tlsSrv,
		pipelineConfig: hpipe.PipelineConfig{
			AdapterID: "default",
			Channels:  map[string]channel.Channel{"rest-hook": restCh},
		},
		topics: []hpipe.TopicFixture{{
			URL:     "http://example.org/topics/hl7-passthrough",
			Version: "1.0.0",
			Title:   "HL7 passthrough",
			Body:    []byte(passthroughTopicJSON),
		}},
	})

	const N = 50
	tags := make([]string, N)
	subIDs := make([]uuid.UUID, N)
	for i := 0; i < N; i++ {
		tags[i] = fmt.Sprintf("fanout50-%d-%s", i, uuid.New().String()[:6])
		subIDs[i] = fx.createSubscription(ctx, t, h,
			restHookSub("http://example.org/topics/hl7-passthrough", tlsSrv.URL, tags[i], nil))
	}

	// Drive ONE HL7 message.
	driveAdmit(t, ctx, h, "FANOUT50-1", "MRN-FANOUT-50", "A01")

	// Each subscriber must see exactly one notification.
	for i := 0; i < N; i++ {
		if _, err := WaitForNotification(ctx, h, tags[i], 60*time.Second); err != nil {
			dumpAndFail(t, ctx, h, subIDs[i], "subscriber %d wait: %v", i, err)
		}
	}
	for i := 0; i < N; i++ {
		got := h.MockSub.RestHook.Received(tags[i])
		if len(got) != 1 {
			t.Errorf("subscriber %d (tag %s): expected 1 delivery, got %d",
				i, tags[i], len(got))
		}
	}

	// Each subscription's deliveries.event_number must be exactly [1].
	for i := 0; i < N; i++ {
		nums, err := readEventNumbers(ctx, h, subIDs[i])
		if err != nil {
			t.Fatalf("read event_numbers for %s: %v", tags[i], err)
		}
		if len(nums) != 1 || nums[0] != 1 {
			t.Errorf("subscriber %d event_numbers = %v; want [1]", i, nums)
		}
	}
}

// TestScenario_Pipeline_FanoutMonotonicAcrossThreeMessages drives 3
// HL7 messages and verifies each subscription sees event_numbers
// 1,2,3 in order — no gaps, no duplicates, and the order matches the
// HL7 message order.
//
// This catches regressions where the submatcher's per-subscription
// cursor advance becomes non-monotonic under back-to-back claim-batch
// boundaries (e.g., if the lock granularity is wrong).
func TestScenario_Pipeline_FanoutMonotonicAcrossThreeMessages(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := newDeadline(context.Background(), 120*time.Second)
	defer cancel()

	tlsSrv, err := hpipe.StartTLSRestHookServer(h.MockSub.RestHook.Handler())
	if err != nil {
		t.Fatalf("tls rest-hook: %v", err)
	}
	t.Cleanup(func() { _ = tlsSrv.Close() })
	restCh, err := resthook.New(resthook.Options{HTTPClient: tlsSrv.Client()})
	if err != nil {
		t.Fatalf("resthook.New: %v", err)
	}

	fx := newScenarioFixture(t, ctx, h, scenarioConfig{
		preBuiltTLS: tlsSrv,
		pipelineConfig: hpipe.PipelineConfig{
			AdapterID: "default",
			Channels:  map[string]channel.Channel{"rest-hook": restCh},
		},
		topics: []hpipe.TopicFixture{{
			URL:     "http://example.org/topics/hl7-passthrough",
			Version: "1.0.0",
			Title:   "HL7 passthrough",
			Body:    []byte(passthroughTopicJSON),
		}},
	})

	const subs = 5
	const msgs = 3
	tags := make([]string, subs)
	subIDs := make([]uuid.UUID, subs)
	for i := 0; i < subs; i++ {
		tags[i] = fmt.Sprintf("fanout-mono-%d-%s", i, uuid.New().String()[:6])
		subIDs[i] = fx.createSubscription(ctx, t, h,
			restHookSub("http://example.org/topics/hl7-passthrough", tlsSrv.URL, tags[i], nil))
	}

	// Drive 3 sequential HL7 messages with distinct MSH-10s.
	for j := 0; j < msgs; j++ {
		driveAdmit(t, ctx, h, fmt.Sprintf("MONO-%d", j), fmt.Sprintf("MRN-mono-%d", j), "A01")
	}

	// Wait for each subscription to receive 3.
	for i := 0; i < subs; i++ {
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if len(h.MockSub.RestHook.Received(tags[i])) >= msgs {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		got := h.MockSub.RestHook.Received(tags[i])
		if len(got) != msgs {
			dumpAndFail(t, ctx, h, subIDs[i],
				"subscriber %d: got %d notifications, want %d", i, len(got), msgs)
		}
	}

	// Each subscription's event_numbers must be exactly 1,2,3.
	for i := 0; i < subs; i++ {
		nums, err := readEventNumbers(ctx, h, subIDs[i])
		if err != nil {
			t.Fatalf("read for %s: %v", tags[i], err)
		}
		if len(nums) != msgs {
			t.Errorf("subscriber %d: %d deliveries, want %d (event_numbers=%v)",
				i, len(nums), msgs, nums)
			continue
		}
		for j, n := range nums {
			if n != int64(j+1) {
				t.Errorf("subscriber %d delivery[%d]: event_number=%d want %d",
					i, j, n, j+1)
			}
		}
	}
}

// TestScenario_Pipeline_AdapterPanicDeadLetters_OtherMessagesFlow
// verifies the production guarantee: when an adapter callback panics
// mid-translate, the panicking message is dead-lettered (does NOT
// crash the worker) and unrelated messages continue flowing.
//
// Why this matters: a vendor adapter is third-party code. A NPE deep
// in the vendor's lex path must not take down the host process and
// stall every other tenant's pipeline.
func TestScenario_Pipeline_AdapterPanicDeadLetters_OtherMessagesFlow(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := newDeadline(context.Background(), 120*time.Second)
	defer cancel()

	// Adapter that panics on messages whose MSH-10 contains "BOMB"
	// and otherwise behaves like a passthrough Bundle.
	var panics atomic.Int64
	adapter := &hpipe.ScriptedAdapter{
		ResourceType: "Bundle",
		ClassifyFn: func(raw []byte) spi.Classification {
			if containsBytes(raw, []byte("BOMB")) {
				panics.Add(1)
				panic("vendor adapter exploded on tagged input")
			}
			return spi.Classification{Kind: spi.ChangeCreate}
		},
		BodyFn: func(raw []byte) []byte {
			return []byte(`{"resourceType":"Bundle","type":"collection"}`)
		},
	}

	tlsSrv, err := hpipe.StartTLSRestHookServer(h.MockSub.RestHook.Handler())
	if err != nil {
		t.Fatalf("tls rest-hook: %v", err)
	}
	t.Cleanup(func() { _ = tlsSrv.Close() })
	restCh, err := resthook.New(resthook.Options{HTTPClient: tlsSrv.Client()})
	if err != nil {
		t.Fatalf("resthook.New: %v", err)
	}

	fx := newScenarioFixture(t, ctx, h, scenarioConfig{
		preBuiltTLS: tlsSrv,
		pipelineConfig: hpipe.PipelineConfig{
			Adapter:   adapter,
			AdapterID: "default",
			Channels:  map[string]channel.Channel{"rest-hook": restCh},
		},
		topics: []hpipe.TopicFixture{{
			URL:     "http://example.org/topics/hl7-passthrough",
			Version: "1.0.0",
			Title:   "HL7 passthrough",
			Body:    []byte(passthroughTopicJSON),
		}},
	})

	tag := shortTag("panic-isolation")
	subID := fx.createSubscription(ctx, t, h,
		restHookSub("http://example.org/topics/hl7-passthrough", tlsSrv.URL, tag, nil))

	// Drive 1 BOMB message + 2 normal messages, mixed order. The
	// adapter classifier looks for the literal "BOMB" substring in the
	// raw HL7 bytes, so message IDs for the safe messages must NOT
	// contain that substring.
	driveAdmit(t, ctx, h, "ISO-SAFE-1", "MRN-iso-1", "A01")        // safe
	driveAdmit(t, ctx, h, "ISO-PANIC-BOMB", "MRN-iso-bomb", "A01") // BOMB → panic
	driveAdmit(t, ctx, h, "ISO-SAFE-2", "MRN-iso-2", "A01")        // safe after panic

	// Two non-bomb messages must reach the subscriber.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if len(h.MockSub.RestHook.Received(tag)) >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	got := h.MockSub.RestHook.Received(tag)
	if len(got) < 2 {
		dumpAndFail(t, ctx, h, subID,
			"subscriber received %d deliveries; expected >= 2 non-bomb messages",
			len(got))
	}

	// Adapter panic must have actually fired (otherwise the test is
	// vacuous).
	if panics.Load() == 0 {
		t.Errorf("adapter never panicked — test is vacuous")
	}

	// The bomb message must be dead-lettered. dead_letters has columns
	// adapter_id, error_class, etc.; we just need to see ANY row.
	var dlCount int
	if err := h.DB.QueryRow(ctx, `SELECT count(*) FROM dead_letters`).Scan(&dlCount); err != nil {
		t.Fatalf("count dead_letters: %v", err)
	}
	if dlCount == 0 {
		t.Errorf("expected at least one dead_letters row from adapter panic; got 0")
	}
}

// readEventNumbers returns the event_number column of every deliveries
// row for sub, ordered ascending.
func readEventNumbers(ctx context.Context, h *Harness, sub uuid.UUID) ([]int64, error) {
	rows, err := h.DB.Query(ctx,
		`SELECT event_number FROM deliveries WHERE subscription_id=$1 ORDER BY event_number`,
		sub,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// TestE2E_Pipeline_ExactlyOnce_PerSubscription verifies the
// SKIP-LOCKED + monotonic event_number contract by simulating M
// concurrent submatcher claims against the same subscription. This is
// a DB-level invariant test (no full pipeline), but it directly mirrors
// the production claim-loop SQL.
func TestE2E_Pipeline_ExactlyOnce_PerSubscription(t *testing.T) {
	t.Parallel()
	h := requireHarness(t)
	ctx := context.Background()

	subID := h.mustRegisterSubscription(t, ctx, "client-exactly-once")
	const N = 30

	// Fire N concurrent goroutines, each claiming exactly one event for
	// our subscription via the same UPDATE-RETURNING pattern the
	// submatcher uses. ehr_event_id has no FK so we use fresh uuids.
	type result struct {
		eventNum int64
		eventID  uuid.UUID
	}
	results := make(chan result, N)
	errCh := make(chan error, N)
	for i := 0; i < N; i++ {
		ehrID := uuid.New()
		go func() {
			tx, err := h.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			if err != nil {
				errCh <- err
				return
			}
			defer func() { _ = tx.Rollback(ctx) }()

			var n int64
			if err := tx.QueryRow(ctx, `
				UPDATE subscriptions
				   SET next_event_number = next_event_number + 1,
				       updated_at = now()
				 WHERE id = $1
				 RETURNING next_event_number`, subID).Scan(&n); err != nil {
				errCh <- err
				return
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO deliveries
				    (subscription_id, ehr_event_id, event_number, status,
				     attempts, next_attempt_at, correlation_id)
				VALUES ($1, $2, $3, 'pending', 0, now(), $4)`,
				subID, ehrID, n, uuid.New(),
			); err != nil {
				errCh <- err
				return
			}
			if err := tx.Commit(ctx); err != nil {
				errCh <- err
				return
			}
			results <- result{eventNum: n, eventID: ehrID}
		}()
	}

	collected := make([]result, 0, N)
	for i := 0; i < N; i++ {
		select {
		case r := <-results:
			collected = append(collected, r)
		case err := <-errCh:
			t.Fatalf("claim worker: %v", err)
		case <-time.After(30 * time.Second):
			t.Fatalf("timed out waiting for %d/%d claims", i, N)
		}
	}
	if len(collected) != N {
		t.Fatalf("collected %d, want %d", len(collected), N)
	}

	// event_numbers must be exactly 1..N with no duplicates.
	seen := map[int64]bool{}
	for _, r := range collected {
		if seen[r.eventNum] {
			t.Errorf("duplicate event_number=%d", r.eventNum)
		}
		seen[r.eventNum] = true
	}
	if len(seen) != N {
		t.Errorf("got %d distinct event_numbers, want %d", len(seen), N)
	}
	for i := int64(1); i <= int64(N); i++ {
		if !seen[i] {
			t.Errorf("missing event_number=%d", i)
		}
	}

	// Final cursor must be exactly N.
	var cursor int64
	if err := h.DB.QueryRow(ctx,
		`SELECT next_event_number FROM subscriptions WHERE id=$1`, subID,
	).Scan(&cursor); err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if cursor != int64(N) {
		t.Errorf("subscriptions.next_event_number=%d, want %d", cursor, N)
	}
}
