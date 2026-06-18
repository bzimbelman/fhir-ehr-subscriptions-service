// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package pool wraps pgxpool with the storage layer's tuning, defaults,
// and lifecycle. The pool is the only outbound dependency of the
// repositories; nobody else opens connections.
package pool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Config controls pool behavior.
type Config struct {
	// URL is the libpq-style connection string. Required.
	URL string

	// MinConnections kept warm at all times. Default 4.
	MinConnections int32
	// MaxConnections is the upper bound; acquire blocks above this.
	// Default 16.
	MaxConnections int32
	// StatementTimeout is sent via SET on every checked-out connection.
	// Default 30s.
	StatementTimeout time.Duration
	// IdleTimeout — idle conns above min are evicted after this. Default 5m.
	IdleTimeout time.Duration
	// MaxConnectionLifetime — connections recycled after this even under
	// steady demand. Default 30m.
	MaxConnectionLifetime time.Duration
	// AcquireTimeout — how long callers wait before erroring. Default 5s.
	AcquireTimeout time.Duration
	// HealthCheckInterval — background SELECT 1 cadence. Default 30s.
	HealthCheckInterval time.Duration
	// ApplicationName is forwarded to libpq via the connection string.
	// Default "fhir-subscriptions-foss".
	ApplicationName string
}

// ApplyDefaults fills in any zero-valued fields with the documented
// defaults.
func (c *Config) ApplyDefaults() {
	if c.MinConnections == 0 {
		c.MinConnections = 4
	}
	if c.MaxConnections == 0 {
		c.MaxConnections = 16
	}
	if c.StatementTimeout == 0 {
		c.StatementTimeout = 30 * time.Second
	}
	if c.IdleTimeout == 0 {
		c.IdleTimeout = 5 * time.Minute
	}
	if c.MaxConnectionLifetime == 0 {
		c.MaxConnectionLifetime = 30 * time.Minute
	}
	if c.AcquireTimeout == 0 {
		c.AcquireTimeout = 5 * time.Second
	}
	if c.HealthCheckInterval == 0 {
		c.HealthCheckInterval = 30 * time.Second
	}
	if c.ApplicationName == "" {
		c.ApplicationName = "fhir-subscriptions-foss"
	}
}

// Validate returns a typed error if the config is internally inconsistent.
func (c Config) Validate() error {
	if c.URL == "" {
		return errors.New("pool: URL is required")
	}
	if c.MaxConnections < c.MinConnections {
		return fmt.Errorf("pool: max_connections (%d) < min_connections (%d)",
			c.MaxConnections, c.MinConnections)
	}
	if c.MaxConnections <= 0 {
		return errors.New("pool: max_connections must be > 0")
	}
	return nil
}

// Pool is the storage layer's typed wrapper around *pgxpool.Pool.
// Construct via Open.
type Pool struct {
	cfg  Config
	pgx  *pgxpool.Pool
	stmt string

	mu     sync.Mutex
	closed bool
}

// Open parses the URL, builds a pgxpool.Config, applies the storage
// layer's tuning, and connects.
func Open(ctx context.Context, cfg Config) (*Pool, error) {
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	pcfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("pool: parse url: %w", err)
	}

	pcfg.MinConns = cfg.MinConnections
	pcfg.MaxConns = cfg.MaxConnections
	pcfg.MaxConnLifetime = cfg.MaxConnectionLifetime
	pcfg.MaxConnIdleTime = cfg.IdleTimeout
	pcfg.HealthCheckPeriod = cfg.HealthCheckInterval

	if pcfg.ConnConfig.RuntimeParams == nil {
		pcfg.ConnConfig.RuntimeParams = make(map[string]string)
	}
	pcfg.ConnConfig.RuntimeParams["application_name"] = cfg.ApplicationName

	stmtMillis := cfg.StatementTimeout.Milliseconds()
	stmtSet := fmt.Sprintf("SET statement_timeout = %d", stmtMillis)

	pcfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, stmtSet)
		return err
	}

	pgxp, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("pool: connect: %w", err)
	}

	p := &Pool{cfg: cfg, pgx: pgxp, stmt: stmtSet}
	return p, nil
}

// Pgx returns the underlying *pgxpool.Pool. Repositories use this; the
// rest of the service interacts with Pool directly.
func (p *Pool) Pgx() *pgxpool.Pool {
	return p.pgx
}

// Probe runs SELECT 1 within the given timeout. Returns nil if the pool
// can serve a query.
func (p *Pool) Probe(ctx context.Context, timeout time.Duration) error {
	if p == nil || p.pgx == nil {
		return errors.New("pool: not initialized")
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var ok int
	row := p.pgx.QueryRow(probeCtx, "SELECT 1")
	if err := row.Scan(&ok); err != nil {
		return fmt.Errorf("pool: probe: %w", err)
	}
	if ok != 1 {
		return errors.New("pool: probe: unexpected result")
	}
	return nil
}

// Close shuts the pool. Idempotent.
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	if p.pgx != nil {
		p.pgx.Close()
	}
}

// Config returns the effective config (with defaults applied).
func (p *Pool) Config() Config { return p.cfg }
