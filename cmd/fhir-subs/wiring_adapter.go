// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/mllp"
)

// buildMLLPListener constructs the MLLP TCP listener from cfg. Story #112
// wires per-listener TLS / mTLS through to mllp.ListenerConfig.TLS and
// promotes the previously hardcoded read-idle / inflight / on-persist-fail
// tunables to operator-facing config knobs.
func buildMLLPListener(cfg MLLPConfig, pool *pgxpool.Pool, hl7Q *repos.Hl7MessageQueueRepo, _ *slog.Logger) (*mllp.Listener, error) {
	endpoints := make([]mllp.EndpointConfig, 0, len(cfg.Listeners))
	for _, ep := range cfg.Listeners {
		endpoints = append(endpoints, mllp.EndpointConfig{
			Name:            ep.Name,
			Bind:            ep.Bind,
			ProxyProtocolV2: ep.ProxyProtocolV2,
		})
	}
	maxBytes := cfg.MaxMessageBytes
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	persistTimeout := cfg.PersistTimeout
	if persistTimeout <= 0 {
		persistTimeout = 5 * time.Second
	}
	drain := cfg.ShutdownDrainGrace
	if drain <= 0 {
		drain = 10 * time.Second
	}
	frameAssembly := cfg.FrameAssemblyTimeout
	if frameAssembly <= 0 {
		frameAssembly = 30 * time.Second
	}
	readIdle := cfg.ReadIdleTimeout
	if readIdle <= 0 {
		readIdle = 60 * time.Second
	}
	nackDropAfter := cfg.NackThenDropAfter
	if nackDropAfter <= 0 {
		nackDropAfter = 5
	}
	inflightCap := cfg.InflightCapPerConn
	if inflightCap <= 0 {
		inflightCap = 64
	}
	onPersistFail := mllp.OnPersistFailNack
	if cfg.OnPersistFail == "drop" {
		onPersistFail = mllp.OnPersistFailDrop
	}

	// Build the listener-wide TLS config from the first endpoint that
	// has a TLS block. Validate already enforced that every endpoint
	// with a TLS block matches, so we can take any of them.
	var tlsCfg *mllp.TLSConfig
	for _, ep := range cfg.Listeners {
		if ep.TLS == nil {
			continue
		}
		built, terr := buildMLLPTLSConfig(ep.TLS)
		if terr != nil {
			return nil, fmt.Errorf("mllp tls (%s): %w", ep.Name, terr)
		}
		tlsCfg = built
		break
	}

	listener := mllp.New(mllp.ListenerConfig{
		Endpoints:            endpoints,
		MaxMessageBytes:      maxBytes,
		ReadIdleTimeout:      readIdle,
		PersistTimeout:       persistTimeout,
		FrameAssemblyTimeout: frameAssembly,
		NackThenDropAfter:    nackDropAfter,
		ShutdownDrainGrace:   drain,
		InflightCapPerConn:   inflightCap,
		OnPersistFail:        onPersistFail,
		MaxConnections:       cfg.MaxConnections,
		MaxConnectionsPerIP:  cfg.MaxConnectionsPerIP,
		TLS:                  tlsCfg,
	}, &poolMLLPPersister{pool: pool, repo: hl7Q}, nil, nil)
	return listener, nil
}

// buildMLLPTLSConfig loads the cert/key (and optional client CA) from
// disk and returns a mllp.TLSConfig wrapping a *tls.Config. Real TLS
// only — no fallbacks. Failures here mean the binary refuses to start.
func buildMLLPTLSConfig(src *MLLPListenerTLSConfig) (*mllp.TLSConfig, error) {
	cert, err := tls.LoadX509KeyPair(src.CertFile, src.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load cert/key: %w", err)
	}
	out := &mllp.TLSConfig{
		Config: &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		},
		RequireAndVerifyClientCert: src.RequireClientCert,
	}
	if src.ClientCAFile != "" {
		body, rErr := os.ReadFile(src.ClientCAFile) //nolint:gosec // operator-supplied CA path
		if rErr != nil {
			return nil, fmt.Errorf("read client_ca_file: %w", rErr)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(body) {
			return nil, fmt.Errorf("client_ca_file: no PEM certificates parsed from %s", src.ClientCAFile)
		}
		out.ClientCAs = pool
	}
	return out, nil
}

// poolMLLPPersister adapts the hl7_message_queue repo to the
// mllp.Persister interface. Mirrors the orchestrator harness's
// pgxPersister but ships in production code.
type poolMLLPPersister struct {
	pool *pgxpool.Pool
	repo *repos.Hl7MessageQueueRepo
}

func (p *poolMLLPPersister) Persist(ctx context.Context, row mllp.QueueRow) error {
	_, err := p.repo.Insert(ctx, p.pool, repos.Hl7MessageQueueRow{
		ListenerEndpoint: row.ListenerEndpoint,
		PeerAddr:         row.PeerAddr,
		MllpMessageID:    row.MLLPMessageID,
		CorrelationID:    row.CorrelationID,
		RawBody:          row.Body,
		ReceivedAt:       row.ReceivedAt,
	})
	if err != nil {
		return fmt.Errorf("%w: %v", mllp.ErrPersistTransient, err)
	}
	return nil
}

// pgWebhookSecretResolver adapts the adapter_state KV table to the
// webhook.SecretResolver interface. Each call queries the table fresh
// — operators rotate the per-adapter HMAC secret by upserting
// (adapter_id, scope='webhook', key='secret', value=<plaintext>).
// No in-process cache: rotation has to be observable on the next
// request, and the per-request DB hit on a low-volume vendor-push
// path is acceptable. If rotation cadence ever drives this hot,
// add a TTL'd cache with explicit invalidation.
type pgWebhookSecretResolver struct {
	pool *pgxpool.Pool
	repo *repos.AdapterStateRepo
}

func newPgWebhookSecretResolver(pool *pgxpool.Pool, repo *repos.AdapterStateRepo) *pgWebhookSecretResolver {
	return &pgWebhookSecretResolver{pool: pool, repo: repo}
}

// WebhookSecret satisfies webhook.SecretResolver. Returns ("", false)
// for an unknown adapter, on-error, or when the row exists but the
// stored bytes are empty (operators must upsert a non-empty value).
// Each call hits the DB so a rotation upsert is observed on the next
// request — there is intentionally no in-process cache.
func (p *pgWebhookSecretResolver) WebhookSecret(adapterID string) (string, bool) {
	if p == nil || p.pool == nil || p.repo == nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	row, err := p.repo.Get(ctx, p.pool, adapterID, "webhook", "secret")
	if err != nil || row == nil || len(row.Value) == 0 {
		return "", false
	}
	return string(row.Value), true
}
