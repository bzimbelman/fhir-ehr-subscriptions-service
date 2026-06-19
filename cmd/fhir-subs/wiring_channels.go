// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	chemail "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/email"
	chwebsocket "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/websocket"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// wsTokenAdapter bridges the storage repos.WsBindingTokensRepo to the
// websocket channel's TokenConsumer interface. The two packages declare
// equivalent enum/result types to avoid a layering cycle; this adapter
// translates row-level outcomes to the channel's enum.
type wsTokenAdapter struct {
	pool *pgxpool.Pool
	repo *repos.WsBindingTokensRepo
}

// Consume satisfies websocket.TokenConsumer.
func (a wsTokenAdapter) Consume(ctx context.Context, token string, now time.Time) (chwebsocket.ConsumeResult, error) {
	out, err := a.repo.Consume(ctx, a.pool, token, now)
	if err != nil {
		return chwebsocket.ConsumeResult{}, err
	}
	res := chwebsocket.ConsumeResult{
		SubscriptionID: out.SubscriptionID,
		ClientID:       out.ClientID,
	}
	switch out.Outcome {
	case repos.ConsumeOK:
		res.Outcome = chwebsocket.ConsumeOK
	case repos.ConsumeAlreadyUsed:
		res.Outcome = chwebsocket.ConsumeAlreadyUsed
	case repos.ConsumeExpired:
		res.Outcome = chwebsocket.ConsumeExpired
	default:
		res.Outcome = chwebsocket.ConsumeNotFound
	}
	return res, nil
}

// noopReplayer is the production EventReplayer until the past-events
// store lands. Returning an empty slice is the same observable
// behavior the channel ships today: a reconnecting subscriber gets
// bind-success and zero replay frames. Real replay arrives in a
// follow-up story along with a per-subscription event archive.
type noopReplayer struct{}

// ReplaySince returns no past events. See the type comment for the
// rationale and follow-up tracking.
func (noopReplayer) ReplaySince(_ context.Context, _ uuid.UUID, _ uint64) ([]chwebsocket.PastEvent, error) {
	return nil, nil
}

// buildEmailConfig copies the operator-supplied EmailChannelConfig YAML
// shape into the channel-package Config that internal/channel/email.New
// consumes. Empty strings / zero values fall through to the channel
// package defaults; New surfaces validation errors loud.
func buildEmailConfig(cfg EmailChannelConfig, logger *slog.Logger) chemail.Config {
	out := chemail.Config{
		From:                     cfg.From,
		SubjectTemplate:          cfg.SubjectTemplate,
		SMTPHost:                 cfg.SMTPHost,
		SMTPPort:                 cfg.SMTPPort,
		AllowCleartextAuth:       cfg.AllowCleartextAuth,
		AttachmentThresholdBytes: cfg.AttachmentThresholdBytes,
		RequestTimeout:           cfg.RequestTimeout,
		LocalName:                cfg.LocalName,
		UserAgent:                cfg.UserAgent,
		TLSMinVersion:            cfg.TLSMinVersion,
		Logger:                   logger.With("component", "channel.email"),
		AuthUsername:             cfg.AuthUsername,
		AuthPassword:             cfg.AuthPassword,
		AuthIdentity:             cfg.AuthIdentity,
	}
	if cfg.STARTTLS != "" {
		out.STARTTLS = chemail.STARTTLSPolicy(cfg.STARTTLS)
	}
	if cfg.AuthMechanism != "" {
		out.AuthMechanism = chemail.AuthMechanism(cfg.AuthMechanism)
	}
	return out
}
