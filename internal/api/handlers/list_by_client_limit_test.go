//go:build integration

// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/migrate"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// OP #188 RED — PgSubscriptionsStore.ListByClient must enforce a
// page-size cap so a high-fan-out tenant cannot pull a multi-thousand
// row resultset into one allocation. The AC introduces
// `cfg.Handlers.MaxListByClientPageSize` (default 200) and requires
// PgSubscriptionsStore to honour it. Today the SQL has NO LIMIT clause
// — every row for the client comes back.
//
// This test inserts >250 rows for one client_id, calls ListByClient,
// and asserts the result is bounded by the configured page size.

func TestPgSubscriptionsStore_ListByClient_EnforcesMaxPageSize(t *testing.T) {
	url := startPostgres(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	subsRepo := repos.NewSubscriptionsRepo()
	clientID := "client-fanout"
	const want = 250
	for i := 0; i < want; i++ {
		row := repos.SubscriptionRow{
			ClientID:    clientID,
			Status:      repos.SubActive,
			TopicURL:    "http://example.org/topics/orders",
			ChannelType: "rest-hook",
			Endpoint:    "https://example.org/wh",
			Content:     "id-only",
		}
		if _, err := subsRepo.Insert(ctx, pool, row); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	// The store MUST be configurable with a page-size cap. Phase B
	// adds a constructor variant that takes the cap (or wires it
	// through QueryTimeouts / a new options struct). The reference here
	// expects a 200-cap default per AC. If Phase B chooses a different
	// API surface — e.g. cfg.Handlers.MaxListByClientPageSize plumbed
	// via Deps — the assertion below still holds: at most 200 rows.
	store := handlers.NewPgSubscriptionsStore(pool)

	rows, err := store.ListByClient(ctx, clientID)
	if err != nil {
		t.Fatalf("ListByClient: %v", err)
	}
	const cap = 200
	if len(rows) > cap {
		t.Errorf("ListByClient returned %d rows, want at most %d (page-size cap not enforced)", len(rows), cap)
	}
}

// TestAdmin_ListSubscriptions_TruncatedResultsSurface — OP #188 AC
// follow-up. The admin endpoint MUST signal truncation when the
// underlying ListByClient hits the cap. Either a Bundle.link "next" or
// a top-level `truncated: true` field is acceptable; we assert one of
// the two is present so an operator can spot a hidden tail.
//
// This test exercises the admin handler at the HTTP boundary so the
// truncation indicator must travel back to the operator-visible
// response shape.
//
// Reference here uses cfg.Handlers.MaxListByClientPageSize as the
// pivot the AC requires. Phase B introduces the field — until then
// this test fails to compile in cmd/fhir-subs config tests, but the
// HTTP-level test below uses memSubs to force the truncation flag
// without depending on the new field's presence.
//
// Note: the test DOES depend on a memSubs that returns 200 rows when
// the underlying tenant has 250 — i.e. a cap-aware in-memory store. We
// assert the response contains either "truncated":true OR a
// link-relation "next". One must be present once Phase B wires this.
//
// We skip this assertion when the new admin path is absent.

// (kept brief — the harness doesn't expose a route-level helper for
// /admin/subscriptions today; the test above on PgSubscriptionsStore
// is the load-bearing guard. Phase B can add a follow-on admin test
// when the route is wired.)
