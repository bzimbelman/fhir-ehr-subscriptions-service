// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package hl7processor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// runReaper sweeps pending_pairs for expired rows and flushes them as
// plain delete or create. LLD §4.5.
func (p *Processor) runReaper(ctx context.Context) {
	tick := time.NewTicker(p.cfg.ReaperTickInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if err := p.reapOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			p.deps.Logger.ErrorContext(ctx, "hl7processor: reaper cycle error",
				slog.String("component", "hl7_processor"),
				slog.String("adapter_id", p.cfg.AdapterID),
				slog.String("error", err.Error()),
			)
		}
	}
}

// reapOnce finds expired pending rows and flushes each in its own tx.
func (p *Processor) reapOnce(ctx context.Context) error {
	keys, err := p.peekExpired(ctx)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		p.flushOneExpired(ctx, k.correlationKey, k.listenerEndpoint)
	}
	return nil
}

type pendingKey struct {
	correlationKey   string
	listenerEndpoint string
}

// peekExpired returns the (correlation_key, listener_endpoint) pairs
// whose expires_at has passed. Read-only; the per-key tx in
// flushOneExpired retakes FOR UPDATE to compete with on_partner_arrival.
func (p *Processor) peekExpired(ctx context.Context) ([]pendingKey, error) {
	tx, err := p.deps.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT correlation_key, listener_endpoint
		FROM pending_pairs
		WHERE expires_at <= $1
		ORDER BY expires_at ASC
		LIMIT 64`, p.deps.Now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]pendingKey, 0, 8)
	for rows.Next() {
		var k pendingKey
		if err := rows.Scan(&k.correlationKey, &k.listenerEndpoint); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// flushOneExpired runs the per-row transaction for a single expired pair.
// LLD §4.5.
func (p *Processor) flushOneExpired(ctx context.Context, key, endpoint string) {
	tx, err := p.deps.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		p.deps.Logger.ErrorContext(ctx, "hl7processor: reaper begin tx",
			slog.String("component", "hl7_processor"),
			slog.String("adapter_id", p.cfg.AdapterID),
			slog.String("correlation_key", key),
			slog.String("error", err.Error()),
		)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, ok, err := p.lockPendingForReap(ctx, tx, key, endpoint)
	if err != nil {
		p.deps.Logger.ErrorContext(ctx, "hl7processor: reaper lock",
			slog.String("component", "hl7_processor"),
			slog.String("adapter_id", p.cfg.AdapterID),
			slog.String("correlation_key", key),
			slog.String("error", err.Error()),
		)
		return
	}
	if !ok {
		// Already resolved or swept; another tx won.
		return
	}

	change := p.materializeUnpairedChange(row)

	if _, err := p.deps.HL7Queue.MarkProcessed(ctx, tx, row.SourceMessageID); err != nil {
		p.deps.Logger.ErrorContext(ctx, "hl7processor: reaper mark processed",
			slog.String("component", "hl7_processor"),
			slog.String("adapter_id", p.cfg.AdapterID),
			slog.String("correlation_key", key),
			slog.String("error", err.Error()),
		)
		return
	}
	if err := p.deps.Pending.Delete(ctx, tx, key, endpoint); err != nil {
		p.deps.Logger.ErrorContext(ctx, "hl7processor: reaper delete pending",
			slog.String("component", "hl7_processor"),
			slog.String("adapter_id", p.cfg.AdapterID),
			slog.String("correlation_key", key),
			slog.String("error", err.Error()),
		)
		return
	}
	if err := p.insertResourceChange(ctx, tx, change); err != nil {
		p.deps.Logger.ErrorContext(ctx, "hl7processor: reaper insert resource_changes",
			slog.String("component", "hl7_processor"),
			slog.String("adapter_id", p.cfg.AdapterID),
			slog.String("correlation_key", key),
			slog.String("error", err.Error()),
		)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		p.deps.Logger.ErrorContext(ctx, "hl7processor: reaper commit",
			slog.String("component", "hl7_processor"),
			slog.String("adapter_id", p.cfg.AdapterID),
			slog.String("correlation_key", key),
			slog.String("error", err.Error()),
		)
		return
	}
	p.metrics().Inc(MetricPairsExpired, map[string]string{"resource_type": change.ResourceType})
	p.metrics().Inc(MetricResourceChangesTotal, map[string]string{
		"adapter_id":    p.cfg.AdapterID,
		"change_kind":   string(change.ChangeKind),
		"resource_type": change.ResourceType,
	})
}

// reapedPending bundles the decoded pending_pairs row plus the held
// FhirResource and source-row correlation_id so the reaper can build the
// resource_change without a re-read.
type reapedPending struct {
	repos.PendingPairRow
	Resource          spi.FhirResource
	HeldCorrelationID uuid.UUID
}

// lockPendingForReap takes FOR UPDATE on a pending_pairs row. Returns
// ok=false when no row exists (already resolved or swept).
func (p *Processor) lockPendingForReap(ctx context.Context, tx pgx.Tx, key, endpoint string) (reapedPending, bool, error) {
	const sql = `
		SELECT correlation_key, listener_endpoint, pending_resource, pending_kind,
		       source_message_id, expires_at, created_at, key_version
		FROM pending_pairs
		WHERE correlation_key = $1 AND listener_endpoint = $2 AND expires_at <= $3
		FOR UPDATE`
	var row repos.PendingPairRow
	var enc []byte
	var kind string
	err := tx.QueryRow(ctx, sql, key, endpoint, p.deps.Now()).Scan(
		&row.CorrelationKey, &row.ListenerEndpoint, &enc, &kind,
		&row.SourceMessageID, &row.ExpiresAt, &row.CreatedAt, &row.KeyVersion,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return reapedPending{}, false, nil
	}
	if err != nil {
		return reapedPending{}, false, err
	}
	row.PendingKind = repos.PendingKind(kind)
	body, err := p.deps.Codec.Decrypt(enc, row.KeyVersion)
	if err != nil {
		return reapedPending{}, false, fmt.Errorf("decrypt pending: %w", err)
	}
	resource, err := decodePendingResource(body)
	if err != nil {
		return reapedPending{}, false, fmt.Errorf("decode pending: %w", err)
	}
	corr, err := lookupCorrelationID(ctx, tx, row.SourceMessageID)
	if err != nil {
		return reapedPending{}, false, fmt.Errorf("lookup held correlation_id: %w", err)
	}
	return reapedPending{PendingPairRow: row, Resource: resource, HeldCorrelationID: corr}, true, nil
}

// materializeUnpairedChange asks the SPI what to emit for a lone pending
// half. LLD §4.5: defaults are plain Delete (cancellation) or plain
// Create (replacement). Vendor adapters may override.
func (p *Processor) materializeUnpairedChange(r reapedPending) spi.ResourceChange {
	occurredAt := r.CreatedAt
	switch r.PendingKind {
	case repos.PendingDelete:
		ch := p.deps.Adapter.OnUnpairedCancellation(r.Resource, r.HeldCorrelationID, occurredAt)
		ch.OccurredAt = occurredAt
		ch.CorrelationID = r.HeldCorrelationID
		if ch.ResourceType == "" {
			ch.ResourceType = r.Resource.ResourceType
		}
		return ch
	case repos.PendingCreate:
		ch := p.deps.Adapter.OnUnpairedReplacement(r.Resource, r.HeldCorrelationID, occurredAt)
		ch.OccurredAt = occurredAt
		ch.CorrelationID = r.HeldCorrelationID
		if ch.ResourceType == "" {
			ch.ResourceType = r.Resource.ResourceType
		}
		return ch
	default:
		// Defensive — schema constraint prevents anything else, but
		// fall through to a plain delete with the held body.
		return spi.ResourceChange{
			ResourceType:  r.Resource.ResourceType,
			ChangeKind:    spi.ChangeDelete,
			Resource:      r.Resource,
			OccurredAt:    occurredAt,
			CorrelationID: r.HeldCorrelationID,
		}
	}
}
