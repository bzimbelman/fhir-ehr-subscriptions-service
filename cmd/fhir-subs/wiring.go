// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	defaultadapter "github.com/bzimbelman/fhir-ehr-subscriptions-service/adapters/default"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/registry"
	adapterspi "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	chresthook "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/resthook"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/builder"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/scheduler"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/submatcher"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/hl7processor"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/lifecycle"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/codec"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/migrate"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/matcher"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/mllp"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/topics/catalog"
)

// productionRuntime aggregates everything the production binary stands
// up when the operator supplies a complete config (database.url + codec
// keys). The fields are owned by the wiring; Shutdown closes them in
// reverse construction order.
type productionRuntime struct {
	pool        *pgxpool.Pool
	codec       *codec.Codec
	authVerif   *auth.Verifier
	tokenSrv    *auth.TokenEndpoint
	router      http.Handler
	mllpListen  *mllp.Listener
	processor   *hl7processor.Processor
	matcher     *matcher.Worker
	submatcher  *submatcher.Worker
	scheduler   *scheduler.Worker
	pipelineWG  sync.WaitGroup
	cancelLoops context.CancelFunc

	// activationWG is joined during shutdown so in-flight subscription
	// activation goroutines either finish, time out, or are cancelled
	// before the process exits (B-10).
	activationWG sync.WaitGroup

	logger *slog.Logger
}

// buildProductionRuntime constructs the full production stack from cfg.
// It is invoked by runWithHooks when cfg.Database.URL is non-empty. On
// any failure it tears down anything already opened so the caller can
// surface a fatal error without leaking handles.
func buildProductionRuntime(ctx context.Context, cfg *Config, logger *slog.Logger, lcMod *lifecycle.LifecycleModule) (*productionRuntime, error) {
	rt := &productionRuntime{logger: logger}

	// --- 1. Postgres pool + migrations -----------------------------------
	pool, err := pgxpool.New(ctx, cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("database: open: %w", err)
	}
	rt.pool = pool

	// Ping under a tight bound so a misconfigured DB at startup fails
	// fast and the orchestrator restarts the pod, instead of hanging
	// the listener registration past the operator's startup probe.
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pingErr := make(chan error, 1)
	go func() { pingErr <- pool.Ping(pingCtx) }()
	select {
	case err := <-pingErr:
		if err != nil {
			rt.shutdown(context.Background())
			return nil, fmt.Errorf("database: ping: %w", err)
		}
	case <-pingCtx.Done():
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("database: ping: %w", pingCtx.Err())
	}

	migCtx, cancelMig := context.WithTimeout(ctx, 60*time.Second)
	if err := migrate.Up(migCtx, pool); err != nil {
		cancelMig()
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("database: migrate: %w", err)
	}
	cancelMig()

	// --- 2. Codec --------------------------------------------------------
	cdc, err := buildCodec(cfg.Codec)
	if err != nil {
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("codec: %w", err)
	}
	rt.codec = cdc

	// --- 3. Adapter registry --------------------------------------------
	adReg := registry.New()
	if err := adReg.Register("default", func() adapterspi.EhrAdapter { return defaultadapter.New() }); err != nil {
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("adapter registry: %w", err)
	}
	loadCfg := registry.LoadConfig{
		AdapterID:  cfg.Adapter.ID,
		HostSpiVer: adapterspi.HostSPIVersion,
	}
	if cfg.Adapter.VersionPin != "" {
		pin := cfg.Adapter.VersionPin
		loadCfg.VersionPin = &pin
	}
	loadedAdapter, err := adReg.Load(ctx, loadCfg)
	if err != nil {
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("adapter load: %w", err)
	}
	hl7Proc := loadedAdapter.BuildHl7Processor(adapterspi.AdapterContext{
		AdapterID: cfg.Adapter.ID,
		Now:       time.Now,
	})

	// --- 4. Repos --------------------------------------------------------
	hl7Q := repos.NewHl7MessageQueueRepo(cdc)
	rcs := repos.NewResourceChangesRepo(cdc)
	ehrEvts := repos.NewEhrEventsRepo(cdc)
	dlv := repos.NewDeliveriesRepo()
	dl := repos.NewDeadLettersRepo(cdc)
	pendingPairs := repos.NewPendingPairsRepo(cdc)
	subsRepo := repos.NewSubscriptionsRepo()
	authClients := repos.NewAuthClientsRepo()

	// --- 5. Auth verifier + token endpoint ------------------------------
	verif, tokenSrv, err := buildAuthEndpoints(cfg.Auth, pool, authClients)
	if err != nil {
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("auth: %w", err)
	}
	rt.authVerif = verif
	rt.tokenSrv = tokenSrv

	// --- 6. Channels: rest-hook (production default) --------------------
	rhCh, err := chresthook.New(chresthook.Options{
		UserAgent:      cfg.Channels.RestHook.UserAgent,
		RequestTimeout: cfg.Channels.RestHook.RequestTimeout,
		Logger:         logger.With("component", "channel.resthook"),
	})
	if err != nil {
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("channel resthook: %w", err)
	}
	chReg := scheduler.NewMapRegistry()
	chReg.Register("rest-hook", rhCh)

	// --- 7. API router (handlers.RegisterRoutes) ------------------------
	urlValidator := handlers.NewURLValidator(handlers.URLValidatorConfig{
		AllowHTTP: cfg.Auth.AllowInsecure, // dev convenience: if insecure JWKS allowed, allow http endpoints too
	})
	channels := handlers.ChannelRegistry{
		"rest-hook": defaultActivator{},
		"websocket": defaultActivator{},
		"email":     defaultActivator{},
	}
	deps := handlers.Deps{
		Subscriptions:       handlers.NewPgSubscriptionsStore(pool),
		Topics:              handlers.NewPgTopicsStore(pool),
		Events:              handlers.NewPgEventsStore(pool),
		Deliveries:          handlers.NewPgDeliveriesStore(pool),
		WsTokens:            handlers.NewPgWsBindingTokensStore(pool),
		Audit:               handlers.NewPgAuditStore(pool),
		Channels:            channels,
		Now:                 func() time.Time { return time.Now().UTC() },
		WSBindingTTL:        5 * time.Minute,
		BaseURL:             "https://" + cfg.Server.HTTP.Bind,
		WSBaseURL:           "wss://" + cfg.Server.HTTP.Bind + "/ws",
		ServerVersion:       Version,
		URLValidator:        urlValidator,
		LifecycleCtx:        ctx,
		ActivationTimeout:   30 * time.Second,
		ActivationWaitGroup: &rt.activationWG,
	}

	r := chi.NewRouter()
	if verif != nil {
		r.Use(verif.Middleware)
	}
	handlers.RegisterRoutes(r, deps)
	if tokenSrv != nil {
		r.Method(http.MethodPost, "/token", tokenSrv)
	}
	rt.router = r

	// --- 8. MLLP listener (optional) ------------------------------------
	if len(cfg.MLLP.Listeners) > 0 {
		listener, err := buildMLLPListener(cfg.MLLP, pool, hl7Q, logger)
		if err != nil {
			rt.shutdown(context.Background())
			return nil, fmt.Errorf("mllp: %w", err)
		}
		if err := listener.Start(ctx); err != nil {
			rt.shutdown(context.Background())
			return nil, fmt.Errorf("mllp start: %w", err)
		}
		rt.mllpListen = listener
	}

	// --- 9. Pipeline workers --------------------------------------------
	loopCtx, cancelLoops := context.WithCancel(context.Background())
	rt.cancelLoops = cancelLoops

	// HL7 processor.
	processorPoll := cfg.Pipeline.HL7Processor.IdlePollInterval
	if processorPoll == 0 {
		processorPoll = 200 * time.Millisecond
	}
	correlHold := cfg.Pipeline.CorrelationHoldWindow
	if correlHold == 0 {
		correlHold = 30 * time.Second
	}
	proc, err := hl7processor.New(hl7processor.Config{
		AdapterID:             cfg.Adapter.ID,
		ClaimBatchSize:        nonZeroInt32(cfg.Pipeline.HL7Processor.ClaimBatchSize, 16),
		ClaimIdlePollInterval: processorPoll,
		ReaperTickInterval:    processorPoll,
		CorrelationHoldWindow: correlHold,
	}, hl7processor.Deps{
		Pool:       pool,
		Codec:      cdc,
		HL7Queue:   hl7Q,
		Pending:    pendingPairs,
		Changes:    rcs,
		DeadLetter: dl,
		Adapter:    hl7Proc,
		Logger:     logger.With("component", "hl7processor"),
	})
	if err != nil {
		cancelLoops()
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("hl7processor: %w", err)
	}
	rt.processor = proc

	// Matcher.
	emptyCat, err := catalog.Load(catalog.Sources{})
	if err != nil {
		cancelLoops()
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("catalog: %w", err)
	}
	cp := matcher.NewAtomicCatalogProvider(emptyCat.Catalog)
	matchPoll := cfg.Pipeline.Matcher.IdlePollInterval
	if matchPoll == 0 {
		matchPoll = 200 * time.Millisecond
	}
	matcherWorker := matcher.NewWorker(pool, rcs, ehrEvts, cp.AsProvider(), matcher.Config{
		ClaimBatchSize:   nonZeroInt32(cfg.Pipeline.Matcher.ClaimBatchSize, 16),
		IdlePollInterval: matchPoll,
	})
	rt.matcher = matcherWorker

	// Submatcher.
	subPoll := cfg.Pipeline.Submatcher.IdlePollInterval
	if subPoll == 0 {
		subPoll = 200 * time.Millisecond
	}
	submatcherWorker := submatcher.NewWorker(pool, subsRepo, ehrEvts, dlv, submatcher.Config{
		ClaimBatchSize:   nonZeroInt32(cfg.Pipeline.Submatcher.ClaimBatchSize, 16),
		IdlePollInterval: subPoll,
	})
	rt.submatcher = submatcherWorker

	// Scheduler.
	schedPoll := cfg.Pipeline.Scheduler.IdlePollInterval
	if schedPoll == 0 {
		schedPoll = 200 * time.Millisecond
	}
	schedulerWorker := scheduler.NewWorker(
		pool, subsRepo, ehrEvts, dlv, dl, chReg,
		builder.New(builder.Config{}),
		scheduler.Config{
			ClaimBatchSize:   nonZeroInt32(cfg.Pipeline.Scheduler.ClaimBatchSize, 16),
			IdlePollInterval: schedPoll,
			Retry: scheduler.RetryConfig{
				Initial:     1 * time.Second,
				Max:         30 * time.Second,
				Min:         500 * time.Millisecond,
				MaxAttempts: 8,
			},
		},
		scheduler.Options{
			Logger: logger.With("component", "scheduler"),
		},
	)
	rt.scheduler = schedulerWorker

	// Launch the loops.
	rt.pipelineWG.Add(4)
	go func() { defer rt.pipelineWG.Done(); _ = proc.Run(loopCtx) }()
	go func() { defer rt.pipelineWG.Done(); _ = matcherWorker.Run(loopCtx) }()
	go func() { defer rt.pipelineWG.Done(); _ = submatcherWorker.Run(loopCtx) }()
	go func() { defer rt.pipelineWG.Done(); _ = schedulerWorker.Run(loopCtx) }()

	// --- 10. Lifecycle shutdown wiring ----------------------------------
	rt.registerLifecycle(lcMod, cfg.Lifecycle.ShutdownGracePeriod)

	// --- 11. Lifecycle readiness check (DB ping) ------------------------
	lcMod.RegisterReadiness("database", func(ctx context.Context) error {
		pingCtx, c := context.WithTimeout(ctx, 2*time.Second)
		defer c()
		return pool.Ping(pingCtx)
	})

	return rt, nil
}

// registerLifecycle wires the runtime's components into the lifecycle
// module's shutdown sequencer:
//
//   - PhaseStopAccepting: MLLP listener stops accepting (Phase 2).
//   - PhaseDrainInFlight: pipeline workers drain (Phase 3, 70% budget).
//   - PhaseCloseConnections: DB pool closes last (Phase 4).
func (r *productionRuntime) registerLifecycle(lcMod *lifecycle.LifecycleModule, grace time.Duration) {
	if grace <= 0 {
		grace = 30 * time.Second
	}

	if r.mllpListen != nil {
		lcMod.RegisterShutdown(lifecycle.ShutdownHook{
			Name:  "mllp.stop_accepting",
			Phase: lifecycle.PhaseStopAccepting,
			Run: func(ctx context.Context) error {
				return r.mllpListen.Shutdown(ctx)
			},
		})
	}

	lcMod.RegisterShutdown(lifecycle.ShutdownHook{
		Name:  "pipeline.drain",
		Phase: lifecycle.PhaseDrainInFlight,
		Run: func(ctx context.Context) error {
			if r.cancelLoops != nil {
				r.cancelLoops()
			}
			done := make(chan struct{})
			go func() {
				r.pipelineWG.Wait()
				close(done)
			}()
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})

	lcMod.RegisterShutdown(lifecycle.ShutdownHook{
		Name:  "api.activations.drain",
		Phase: lifecycle.PhaseDrainInFlight,
		Run: func(ctx context.Context) error {
			done := make(chan struct{})
			go func() {
				r.activationWG.Wait()
				close(done)
			}()
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})

	lcMod.RegisterShutdown(lifecycle.ShutdownHook{
		Name:  "database.close",
		Phase: lifecycle.PhaseCloseConnections,
		Run: func(_ context.Context) error {
			if r.pool != nil {
				r.pool.Close()
			}
			return nil
		},
	})
}

// shutdown performs an immediate teardown for the buildProductionRuntime
// failure paths. Once the runtime has been registered with the lifecycle
// module, shutdown is driven by the sequencer and this method is not
// called.
func (r *productionRuntime) shutdown(ctx context.Context) {
	if r == nil {
		return
	}
	if r.cancelLoops != nil {
		r.cancelLoops()
	}
	if r.mllpListen != nil {
		_ = r.mllpListen.Shutdown(ctx)
	}
	if r.pool != nil {
		r.pool.Close()
	}
}

// buildCodec parses the configured key bundle, decodes the base64
// material, and constructs a Codec.
func buildCodec(cfg CodecConfig) (*codec.Codec, error) {
	if len(cfg.Keys) == 0 {
		return nil, errors.New("at least one key required")
	}
	if cfg.ActiveKeyVersion == 0 {
		return nil, errors.New("active_key_version is required")
	}
	keys := make(map[int32][]byte, len(cfg.Keys))
	for _, k := range cfg.Keys {
		if k.Version == 0 {
			return nil, fmt.Errorf("key entry missing version")
		}
		raw, err := base64.StdEncoding.DecodeString(k.Material)
		if err != nil {
			return nil, fmt.Errorf("key v%d: base64 decode: %w", k.Version, err)
		}
		if len(raw) != 32 {
			return nil, fmt.Errorf("key v%d: want 32 bytes, got %d", k.Version, len(raw))
		}
		keys[k.Version] = raw
	}
	if _, ok := keys[cfg.ActiveKeyVersion]; !ok {
		return nil, fmt.Errorf("active_key_version=%d not present in keys[]", cfg.ActiveKeyVersion)
	}
	return codec.New(codec.NewStaticKeyProvider(keys, cfg.ActiveKeyVersion))
}

// buildAuthEndpoints constructs the verifier and token endpoint from
// config. Returns (nil, nil, nil) when audience is empty (auth disabled
// — not allowed in production but useful for the probe-only fallback).
func buildAuthEndpoints(cfg AuthConfig, pool *pgxpool.Pool, clients *repos.AuthClientsRepo) (*auth.Verifier, *auth.TokenEndpoint, error) {
	if cfg.Audience == "" {
		return nil, nil, nil
	}
	lookup := &poolClientLookup{pool: pool, repo: clients}

	var issuedSecret []byte
	if cfg.IssuedSecret != "" {
		decoded, err := base64.StdEncoding.DecodeString(cfg.IssuedSecret)
		if err != nil {
			return nil, nil, fmt.Errorf("issued_secret base64 decode: %w", err)
		}
		issuedSecret = decoded
	}

	verif, err := auth.NewVerifier(auth.VerifierConfig{
		Audience:          cfg.Audience,
		ClockSkew:         cfg.ClockSkew,
		ClientLookup:      lookup,
		JWKSCacheTTL:      cfg.JWKSCacheTTL,
		IssuedSecret:      issuedSecret,
		IssuedIssuer:      cfg.IssuedIssuer,
		AllowInsecureJWKS: cfg.AllowInsecure,
		JWKSAllowedHosts:  cfg.JWKSAllowed,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("verifier: %w", err)
	}

	if cfg.TokenURL == "" || len(issuedSecret) == 0 {
		return verif, nil, nil
	}
	te, err := auth.NewTokenEndpoint(auth.TokenEndpointConfig{
		Audience:          cfg.Audience,
		TokenURL:          cfg.TokenURL,
		AccessTokenSecret: issuedSecret,
		AccessTokenTTL:    cfg.AccessTokenTTL,
		AccessTokenIssuer: cfg.IssuedIssuer,
		ClientLookup:      lookup,
		JWKSCacheTTL:      cfg.JWKSCacheTTL,
		ClockSkew:         cfg.ClockSkew,
		AllowInsecureJWKS: cfg.AllowInsecure,
		JWKSAllowedHosts:  cfg.JWKSAllowed,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("token endpoint: %w", err)
	}
	return verif, te, nil
}

// poolClientLookup adapts repos.AuthClientsRepo to the auth.ClientLookup
// interface. The verifier's ClientLookup signature is (ctx, id) — repos
// take an explicit Querier — so this thin wrapper supplies the pool.
type poolClientLookup struct {
	pool *pgxpool.Pool
	repo *repos.AuthClientsRepo
}

func (p *poolClientLookup) GetByID(ctx context.Context, id string) (*auth.ClientRecord, error) {
	row, err := p.repo.GetByID(ctx, p.pool, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	return &auth.ClientRecord{
		ID:      row.ID,
		JwksURL: row.JwksURL,
		Scopes:  row.Scopes,
	}, nil
}

// buildMLLPListener constructs the MLLP TCP listener from cfg.
func buildMLLPListener(cfg MLLPConfig, pool *pgxpool.Pool, hl7Q *repos.Hl7MessageQueueRepo, _ *slog.Logger) (*mllp.Listener, error) {
	endpoints := make([]mllp.EndpointConfig, 0, len(cfg.Listeners))
	for _, ep := range cfg.Listeners {
		endpoints = append(endpoints, mllp.EndpointConfig{
			Name: ep.Name,
			Bind: ep.Bind,
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
	listener := mllp.New(mllp.ListenerConfig{
		Endpoints:           endpoints,
		MaxMessageBytes:     maxBytes,
		ReadIdleTimeout:     30 * time.Second,
		PersistTimeout:      persistTimeout,
		NackThenDropAfter:   5,
		ShutdownDrainGrace:  drain,
		InflightCapPerConn:  64,
		OnPersistFail:       mllp.OnPersistFailNack,
		MaxConnections:      cfg.MaxConnections,
		MaxConnectionsPerIP: cfg.MaxConnectionsPerIP,
	}, &poolMLLPPersister{pool: pool, repo: hl7Q}, nil, nil)
	return listener, nil
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

// defaultActivator is the production placeholder for per-channel
// activation handshakes. It always returns HandshakeSucceeded so the
// API flips a freshly-created subscription from `requested` to
// `active`. Real handshake plumbing (FHIR R5 handshake bundle to
// rest-hook subscribers, etc.) is owned by each channel module's own
// activation logic and replaces this default in a follow-up. Without
// the placeholder, every newly created subscription would stay stuck at
// `requested` and the submatcher's "active subscriptions" filter would
// never see it.
type defaultActivator struct{}

// ActivateSubscription always succeeds. See the type-level comment for
// why this is the production default today.
func (defaultActivator) ActivateSubscription(_ context.Context, _ repos.SubscriptionRow) (handlers.HandshakeOutcome, error) {
	return handlers.HandshakeSucceeded, nil
}

// nonZeroInt32 returns v if v > 0, else fallback.
func nonZeroInt32(v, fallback int32) int32 {
	if v > 0 {
		return v
	}
	return fallback
}

// silence the unused-import diagnostics emitted while the package
// scaffolding is still being written. The references here go away once
// the e2e harness exercises every component.
var _ channel.Channel = (channel.Channel)(nil)
