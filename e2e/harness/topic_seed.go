// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/topics/catalog"
)

// TopicFixture is a JSON SubscriptionTopic the harness loads into both
// the in-memory matcher catalog and the subscription_topics table so the
// API's createSubscription handler can find it active.
type TopicFixture struct {
	URL     string
	Version string
	Title   string
	Body    []byte // raw SubscriptionTopic JSON
}

// SeedTopic loads the topic into the catalog returned by Catalog (so
// the matcher's CatalogProvider sees it on the next tick) AND inserts a
// matching row into subscription_topics so the API treats it as active.
//
// Calling SeedTopic multiple times produces a fresh catalog each call;
// the previous catalog handle is discarded. The catalog reload is
// atomic from the matcher's perspective because matcher.Worker reads
// the catalog at the top of every tick via a CatalogProvider closure.
func (p *Pipeline) SeedTopic(ctx context.Context, fx TopicFixture) error {
	// Build the in-memory catalog.
	rep, err := catalog.Load(catalog.Sources{
		BuiltIn: append(append([]catalog.RawTopic(nil), p.builtinTopics...), catalog.RawTopic{
			Origin: "harness/" + fx.URL,
			Bytes:  fx.Body,
		}),
	})
	if err != nil {
		return fmt.Errorf("harness: catalog load: %w", err)
	}
	if len(rep.Rejected) != 0 {
		return fmt.Errorf("harness: topic rejected: %#v", rep.Rejected)
	}

	p.builtinTopics = append(p.builtinTopics, catalog.RawTopic{
		Origin: "harness/" + fx.URL,
		Bytes:  fx.Body,
	})

	p.mu.Lock()
	p.catalog = rep.Catalog
	p.mu.Unlock()

	// Insert the row in subscription_topics if not already present.
	if err := upsertActiveTopicRow(ctx, p.pool, fx); err != nil {
		return fmt.Errorf("harness: seed topic row: %w", err)
	}
	return nil
}

func upsertActiveTopicRow(ctx context.Context, pool *pgxpool.Pool, fx TopicFixture) error {
	const sql = `
		INSERT INTO subscription_topics
			(url, version, title, status, source, body, compiled_form)
		VALUES ($1, $2, $3, 'active', 'builtin', $4::jsonb, $5::bytea)
		ON CONFLICT (url, version) DO UPDATE
		  SET status = 'active',
		      title  = EXCLUDED.title,
		      body   = EXCLUDED.body`
	_, err := pool.Exec(ctx, sql, fx.URL, fx.Version, fx.Title, fx.Body, fx.Body)
	return err
}
