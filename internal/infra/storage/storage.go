// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package storage is the Postgres pool and repository layer.
//
// It is the single owner of every SQL string in the codebase. Other
// modules go through Storage and its repositories; nobody outside this
// package calls pgx directly. The storage layer also runs embedded
// migrations at startup, owns the encryption codec for PHI columns,
// and runs background partition maintenance and retention sweeping.
package storage

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/claim"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/codec"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/migrate"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/outbox"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/partition"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/pool"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/retention"
)

// Config bundles every storage tunable. It mirrors the storage.* block
// in the YAML config (see docs/architecture.md and
// docs/low-level-design/storage.md §11).
type Config struct {
	// PostgresURL is required. libpq-style.
	PostgresURL string

	// Pool tuning. Zero values fall back to defaults.
	Pool pool.Config

	// KeyVersions maps key-version int -> 32-byte AES-256 key.
	KeyVersions map[int32][]byte
	// ActiveKey is the key version new writes use.
	ActiveKey int32

	// Retention windows per table. See storage.md §10.
	Retention RetentionConfig

	// Partitioning behavior.
	Partitioning PartitionConfig

	// Lifecycle controls shutdown timing.
	Lifecycle LifecycleConfig
}

// LifecycleConfig controls graceful-shutdown timing for the storage
// layer. Mirrors the lifecycle.* block in the YAML config so the same
// budget the readiness probe uses also bounds Storage.Shutdown.
type LifecycleConfig struct {
	// ShutdownGracePeriod bounds how long Shutdown will wait for
	// background workers to drain AND how long it gives the underlying
	// pool to close before returning. A non-positive value falls back
	// to 30s.
	ShutdownGracePeriod time.Duration
}

// RetentionConfig controls retention sweeper windows.
type RetentionConfig struct {
	Hl7MessageQueue time.Duration
	Deliveries      time.Duration
	DeadLetters     time.Duration
	AuditLog        time.Duration

	// RunInterval is the daily-cadence sleep between sweeps.
	RunInterval time.Duration
	// BatchSize bounds rows deleted per chunk.
	BatchSize int32
	// BatchPause inserts a small sleep between chunks.
	BatchPause time.Duration
	// TickTimeout caps a single sweep; default 6h.
	TickTimeout time.Duration
}

// PartitionConfig controls the partition maintainer.
type PartitionConfig struct {
	AutoDrop             bool
	PartitionLockTimeout time.Duration
	RunInterval          time.Duration
	// TickTimeout caps a single maintenance cycle; default 30m.
	TickTimeout time.Duration

	// PartitionRetention is per-table; defaults to 30d for both
	// resource_changes and ehr_events.
	ResourceChangesRetention time.Duration
	EhrEventsRetention       time.Duration

	// Now is overridable for tests so the rollover behavior at month
	// boundaries can be exercised deterministically without waiting for
	// real wall-clock advance. Production callers leave this nil; the
	// runner falls back to time.Now.
	Now func() time.Time
}

// ApplyDefaults fills in zero-valued fields with sensible defaults from
// the storage.md spec.
func (c *Config) ApplyDefaults() {
	c.Pool.URL = c.PostgresURL
	c.Pool.ApplyDefaults()

	if c.Retention.Hl7MessageQueue == 0 {
		c.Retention.Hl7MessageQueue = 7 * 24 * time.Hour
	}
	if c.Retention.Deliveries == 0 {
		c.Retention.Deliveries = 90 * 24 * time.Hour
	}
	if c.Retention.DeadLetters == 0 {
		c.Retention.DeadLetters = 180 * 24 * time.Hour
	}
	if c.Retention.AuditLog == 0 {
		c.Retention.AuditLog = 7 * 365 * 24 * time.Hour
	}
	if c.Retention.RunInterval == 0 {
		c.Retention.RunInterval = 24 * time.Hour
	}
	if c.Retention.BatchSize == 0 {
		c.Retention.BatchSize = 5000
	}
	if c.Retention.BatchPause == 0 {
		c.Retention.BatchPause = 100 * time.Millisecond
	}

	if c.Partitioning.RunInterval == 0 {
		c.Partitioning.RunInterval = 24 * time.Hour
	}
	if c.Partitioning.PartitionLockTimeout == 0 {
		c.Partitioning.PartitionLockTimeout = 5 * time.Second
	}
	if c.Partitioning.ResourceChangesRetention == 0 {
		c.Partitioning.ResourceChangesRetention = 30 * 24 * time.Hour
	}
	if c.Partitioning.EhrEventsRetention == 0 {
		c.Partitioning.EhrEventsRetention = 30 * 24 * time.Hour
	}

	if c.Lifecycle.ShutdownGracePeriod <= 0 {
		c.Lifecycle.ShutdownGracePeriod = 30 * time.Second
	}
}

// Context is the dependency bundle the storage layer takes at startup.
// Currently empty (logger / metrics injection points reserved); kept as
// a struct so future fields don't break callers.
type Context struct{}

// Storage is the typed handle the rest of the service uses. Construct
// with Start; tear down with Shutdown.
type Storage struct {
	cfg  Config
	pool *pool.Pool
	cdc  *codec.Codec

	// Repositories — accessed via the methods below.
	hl7q     *repos.Hl7MessageQueueRepo
	rcs      *repos.ResourceChangesRepo
	evt      *repos.EhrEventsRepo
	dlv      *repos.DeliveriesRepo
	dl       *repos.DeadLettersRepo
	pp       *repos.PendingPairsRepo
	as       *repos.AdapterStateRepo
	subs     *repos.SubscriptionsRepo
	topics   *repos.SubscriptionTopicsRepo
	clients  *repos.AuthClientsRepo
	wsTok    *repos.WsBindingTokensRepo
	auditLog *repos.AuditLogRepo

	// Background workers.
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	closed bool
}

// Start opens the pool, applies migrations, builds the codec, wires up
// every repository, and launches the partition maintainer + retention
// sweeper.
func Start(ctx context.Context, cfg Config, _ Context) (*Storage, error) {
	cfg.ApplyDefaults()

	if cfg.PostgresURL == "" {
		return nil, errors.New("storage: PostgresURL is required")
	}
	if len(cfg.KeyVersions) == 0 {
		return nil, errors.New("storage: at least one encryption key is required")
	}

	cdc, err := codec.New(codec.NewStaticKeyProvider(cfg.KeyVersions, cfg.ActiveKey))
	if err != nil {
		return nil, fmt.Errorf("storage: codec: %w", err)
	}

	p, err := pool.Open(ctx, cfg.Pool)
	if err != nil {
		return nil, err
	}

	if err := migrate.Up(ctx, p.Pgx()); err != nil {
		p.Close()
		return nil, fmt.Errorf("storage: migrate: %w", err)
	}

	bgCtx, cancel := context.WithCancel(context.Background())
	s := &Storage{
		cfg:    cfg,
		pool:   p,
		cdc:    cdc,
		cancel: cancel,

		hl7q:     repos.NewHl7MessageQueueRepo(cdc),
		rcs:      repos.NewResourceChangesRepo(cdc),
		evt:      repos.NewEhrEventsRepo(cdc),
		dlv:      repos.NewDeliveriesRepo(),
		dl:       repos.NewDeadLettersRepo(cdc),
		pp:       repos.NewPendingPairsRepo(cdc),
		as:       repos.NewAdapterStateRepo(),
		subs:     repos.NewSubscriptionsRepo(),
		topics:   repos.NewSubscriptionTopicsRepo(),
		clients:  repos.NewAuthClientsRepo(),
		wsTok:    repos.NewWsBindingTokensRepo(),
		auditLog: repos.NewAuditLogRepo(),
	}

	// Start partition maintainer. cfg.Partitioning.Now is plumbed
	// through so tests can drive a fast-forward "what does the runner
	// do at month T+4" assertion without waiting for real wall-clock.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		partition.Run(bgCtx, p.Pgx(), partition.Config{
			RunInterval:              cfg.Partitioning.RunInterval,
			LockTimeout:              cfg.Partitioning.PartitionLockTimeout,
			AutoDrop:                 cfg.Partitioning.AutoDrop,
			ResourceChangesRetention: cfg.Partitioning.ResourceChangesRetention,
			EhrEventsRetention:       cfg.Partitioning.EhrEventsRetention,
			TickTimeout:              cfg.Partitioning.TickTimeout,
			Now:                      cfg.Partitioning.Now,
		})
	}()

	// Start retention sweeper.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		retention.Run(bgCtx, p.Pgx(), retention.Config{
			RunInterval:     cfg.Retention.RunInterval,
			BatchSize:       cfg.Retention.BatchSize,
			BatchPause:      cfg.Retention.BatchPause,
			TickTimeout:     cfg.Retention.TickTimeout,
			Hl7MessageQueue: cfg.Retention.Hl7MessageQueue,
			Deliveries:      cfg.Retention.Deliveries,
			DeadLetters:     cfg.Retention.DeadLetters,
			AuditLog:        cfg.Retention.AuditLog,
		})
	}()

	return s, nil
}

// Probe runs SELECT 1. Used by /readyz.
func (s *Storage) Probe(ctx context.Context, timeout time.Duration) error {
	if s == nil || s.pool == nil {
		return errors.New("storage: not initialized")
	}
	return s.pool.Probe(ctx, timeout)
}

// Shutdown stops the background workers and closes the pool. Bounded by
// ctx — if the context expires before the workers drain, the pool is
// still closed, but in a goroutine so a stuck connection cannot pin
// the caller.
func (s *Storage) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	if s.cancel != nil {
		s.cancel()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
	if s.pool != nil {
		// Close in a goroutine bounded by ctx so a stuck connection cannot
		// pin Shutdown indefinitely. Any remaining queries will be
		// canceled when the pool eventually finishes its own Close path.
		closed := make(chan struct{})
		go func() {
			s.pool.Close()
			close(closed)
		}()
		// Last-ditch budget when the caller's ctx has no deadline. We
		// derive it from cfg.Lifecycle.ShutdownGracePeriod so operators
		// can tune the upper bound; the prior hardcoded 5s often raced
		// the rest of the lifecycle's shutdown grace period.
		budget := s.cfg.Lifecycle.ShutdownGracePeriod
		if budget <= 0 {
			budget = 30 * time.Second
		}
		select {
		case <-closed:
		case <-ctx.Done():
			// Caller's context is done — return; pool finishes on its own.
		case <-time.After(budget):
			// Last-ditch budget for a non-context-bound shutdown.
		}
	}
	return nil
}

// Tx is a typed transaction handle. Its only public method is Commit /
// Rollback; everything else flows through repository methods that
// accept a Querier.
type Tx struct {
	tx pgx.Tx
}

// Commit commits the transaction.
func (t *Tx) Commit(ctx context.Context) error { return t.tx.Commit(ctx) }

// Rollback rolls back the transaction.
func (t *Tx) Rollback(ctx context.Context) error { return t.tx.Rollback(ctx) }

// Pgx returns the underlying pgx.Tx for repository methods that need it
// (the FOR UPDATE SKIP LOCKED claim primitive in particular).
func (t *Tx) Pgx() pgx.Tx { return t.tx }

// Begin starts a new transaction. The caller is responsible for
// commit/rollback.
func (s *Storage) Begin(ctx context.Context) (*Tx, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("storage: not initialized")
	}
	tx, err := s.pool.Pgx().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("storage: begin: %w", err)
	}
	return &Tx{tx: tx}, nil
}

// Repository accessors.

// Hl7MessageQueue returns the hl7_message_queue repo.
func (s *Storage) Hl7MessageQueue() *repos.Hl7MessageQueueRepo { return s.hl7q }

// ResourceChanges returns the resource_changes repo.
func (s *Storage) ResourceChanges() *repos.ResourceChangesRepo { return s.rcs }

// EhrEvents returns the ehr_events repo.
func (s *Storage) EhrEvents() *repos.EhrEventsRepo { return s.evt }

// Deliveries returns the deliveries repo.
func (s *Storage) Deliveries() *repos.DeliveriesRepo { return s.dlv }

// DeadLetters returns the dead_letters repo.
func (s *Storage) DeadLetters() *repos.DeadLettersRepo { return s.dl }

// PendingPairs returns the pending_pairs repo.
func (s *Storage) PendingPairs() *repos.PendingPairsRepo { return s.pp }

// AdapterState returns the adapter_state repo.
func (s *Storage) AdapterState() *repos.AdapterStateRepo { return s.as }

// Subscriptions returns the subscriptions repo.
func (s *Storage) Subscriptions() *repos.SubscriptionsRepo { return s.subs }

// SubscriptionTopics returns the subscription_topics repo.
func (s *Storage) SubscriptionTopics() *repos.SubscriptionTopicsRepo { return s.topics }

// AuthClients returns the auth_clients repo.
func (s *Storage) AuthClients() *repos.AuthClientsRepo { return s.clients }

// WsBindingTokens returns the ws_binding_tokens repo.
func (s *Storage) WsBindingTokens() *repos.WsBindingTokensRepo { return s.wsTok }

// AuditLog returns the audit_log repo.
func (s *Storage) AuditLog() *repos.AuditLogRepo { return s.auditLog }

// Pool returns the underlying *pool.Pool. Used by the outbox helper and
// testing utilities.
func (s *Storage) Pool() *pool.Pool { return s.pool }

// Codec returns the column-level encryption codec. Operators that need
// to roll keys outside the normal write path can request the codec
// directly.
func (s *Storage) Codec() *codec.Codec { return s.cdc }

// Outbox runs fn inside a transactional-outbox transaction on the
// pool. Thin re-export of outbox.Run so callers reach the helper via
// the canonical storage handle rather than importing
// internal/infra/storage/outbox directly. Returns an error if the
// storage handle is not initialized.
func (s *Storage) Outbox(ctx context.Context, fn func(ctx context.Context, tx outbox.Tx) error) (outbox.Outcome, error) {
	if s == nil || s.pool == nil {
		return outbox.Outcome{}, errors.New("storage: not initialized")
	}
	return outbox.Run(ctx, s.pool.Pgx(), fn)
}

// ClaimUnprocessed is a generic package-level re-export of
// claim.Unprocessed so the claim sub-package reaches production callers
// via a single storage import. Go does not allow generic methods on
// concrete types, so this is a function rather than a Storage method.
func ClaimUnprocessed[T any](
	ctx context.Context,
	tx pgx.Tx,
	decode func(claim.Scanner) (T, error),
	sql string,
	args ...any,
) ([]T, error) {
	return claim.Unprocessed(ctx, tx, decode, sql, args...)
}
