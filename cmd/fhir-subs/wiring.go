// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/registry"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/scanrunner"
	adapterspi "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/vendorclient"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	apimetrics "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/metrics"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	chemail "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/email"
	chmessage "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/message"
	chresthook "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/resthook"
	chwebsocket "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/websocket"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/builder"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/scheduler"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/submatcher"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/hl7processor"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/lifecycle"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/codec"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/pool"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/matcher"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/mllp"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/topics/catalog"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/webhook"
)

// productionRuntime aggregates everything the production binary stands
// up when the operator supplies a complete config (database.url + codec
// keys). The fields are owned by the wiring; Shutdown closes them in
// reverse construction order.
type productionRuntime struct {
	storage     *storage.Storage
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
	catalogProv *matcher.AtomicCatalogProvider
	topicsDir   string

	// pipeline owns the supervised goroutines for every adapter
	// pipeline worker. Replaces the prior bare `go w.Run(loopCtx)`
	// pattern (story #99): a panic in a worker now bubbles into the
	// supervisor, gets logged + counted, and the worker is restarted
	// with backoff. Status is exposed via /admin/supervisor/status.
	pipeline *supervisedPipeline

	// chReg is the scheduler-side ChannelRegistry. The runtime keeps a
	// reference so the lifecycle "channels.close" hook can fan-call
	// Close on every registered channel during graceful shutdown
	// (stories #101, #102, #103).
	chReg *scheduler.MapRegistry

	// channels holds typed handles for the per-channel Close fan-out
	// in the lifecycle hook. The MapRegistry stores them as
	// channel.Channel; tests probe these directly to assert wiring
	// surfaces.
	wsChannel      *chwebsocket.Channel
	emailChannel   *chemail.Channel
	messageChannel *chmessage.Channel
	rhChannel      *chresthook.Channel

	// rhActivator is the rest-hook handshake activator. The runtime
	// keeps the typed handle so the lifecycle module's
	// PhaseCloseConnections hook can release its idle HTTP connections
	// during graceful shutdown (story #207).
	rhActivator *restHookActivator

	// httpServer is the public HTTP server. run.go sets it via
	// setHTTPServer once the *http.Server is constructed; the
	// PhaseStopAccepting hook registered in registerLifecycle reads it
	// lazily so the hook contract is in place before the server exists
	// (story #207). Nil-safe: if the binary never reaches the
	// http-listening stage, the hook is a no-op.
	httpServerMu sync.Mutex
	httpServer   *http.Server

	// activationWG is joined during shutdown so in-flight subscription
	// activation goroutines either finish, time out, or are cancelled
	// before the process exits (B-10).
	activationWG sync.WaitGroup

	// obsModule owns the observability lifecycle (metrics registry,
	// OTel tracer, audit hash-chain writer, dead-letter reporter).
	// Nil only for failure paths before observability.Start runs;
	// Shutdown is registered with the lifecycle module on success
	// (story #94 AC #2, #6).
	obsModule *observability.ObservabilityModule

	logger *slog.Logger
}

// buildProductionRuntime constructs the full production stack from cfg.
// It is invoked by runWithHooks when cfg.Database.URL is non-empty. On
// any failure it tears down anything already opened so the caller can
// surface a fatal error without leaking handles.
func buildProductionRuntime(ctx context.Context, cfg *Config, logger *slog.Logger, lcMod *lifecycle.LifecycleModule) (*productionRuntime, error) {
	rt := &productionRuntime{logger: logger}

	// --- 1. Storage layer (pool + migrations + codec + background workers) ---
	// storage.Start owns the pool, runs migrations, builds the codec, and
	// launches the partition maintainer + retention sweeper goroutines.
	// Story #95: previously this function opened pgxpool directly and never
	// invoked storage.Start, so the partition + retention runners were
	// dead code. After the third month rollover (migration 0001 bootstraps
	// only 3 partitions) inserts would fail with "no partition for value."
	keys, kerr := decodeCodecKeys(cfg.Codec)
	if kerr != nil {
		return nil, fmt.Errorf("codec: %w", kerr)
	}
	storageCfg := buildStorageConfig(cfg, keys)
	store, err := storage.Start(ctx, storageCfg, storage.Context{})
	if err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}
	rt.storage = store
	rt.pool = store.Pool().Pgx()
	rt.codec = store.Codec()

	// --- 3. Adapter registry --------------------------------------------
	// Story #113 / OP epic #91: register every bundled vendor adapter so
	// an operator config of `adapter.id: <vendor>` resolves at startup.
	// Selection is decided at Load time by cfg.Adapter.ID; registration
	// itself is unconditional.
	adReg := registry.New()
	if regErr := registerAllAdapters(adReg); regErr != nil {
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("adapter registry: %w", regErr)
	}
	// #65: cross-adapter validation runs once at registry init so
	// startup fails loud on capability/builder mismatch or two
	// adapters declaring the same contributed topic url.
	if valErr := adReg.ValidateAll(ctx, adapterspi.HostSPIVersion); valErr != nil {
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("adapter registry: %w", valErr)
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
	cdc := rt.codec
	pool := rt.pool
	hl7Q := repos.NewHl7MessageQueueRepo(cdc)
	rcs := repos.NewResourceChangesRepo(cdc)
	ehrEvts := repos.NewEhrEventsRepo(cdc)
	dlv := repos.NewDeliveriesRepo()
	dl := repos.NewDeadLettersRepo(cdc)
	pendingPairs := repos.NewPendingPairsRepo(cdc)
	subsRepo := repos.NewSubscriptionsRepo()
	authClients := repos.NewAuthClientsRepo()

	// --- 4b. Observability (metrics + tracing + audit + dead-letter
	// reporter). Build the audit Store first so observability.Start can
	// hand the hash-chained writer back through Handles.Audit; the
	// returned module owns its lifetime and is shut down by the
	// lifecycle sequencer (story #94).
	auditStore, err := audit.NewPgStore(pool, audit.PgStoreOptions{})
	if err != nil {
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("audit store: %w", err)
	}
	obsCfg := buildObservabilityConfig(cfg)
	obsClock := func() time.Time { return time.Now().UTC() }
	obsMod, obsHandles, err := observability.Start(ctx, obsCfg, observability.Context{
		StoragePool: auditStore,
		Clock:       obsClock,
	})
	if err != nil {
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("observability: %w", err)
	}
	rt.obsModule = obsMod
	// Build the API metrics set once the registry exists; this is the
	// MetricsRecorder shape the handlers consume (story #94 AC #4).
	apiMetrics, err := apimetrics.New(obsMod.Registry())
	if err != nil {
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("api metrics: %w", err)
	}
	// Boot audit event — exercises the hash-chained writer at startup
	// so operators always see at least one row in audit_log even before
	// the first API request. The chain genesis is verified here and
	// catches a misconfigured durable store loud at boot rather than
	// on the first user-visible failure.
	if bootErr := obsHandles.Audit.Emit(ctx, observability.AuditEvent{
		OccurredAt: obsClock(),
		ActorKind:  "system",
		ActorID:    cfg.Deployment.FacilityID,
		Action:     "service.started",
		TargetKind: "service",
		TargetID:   "fhir-subs",
		Outcome:    "success",
		Payload: map[string]any{
			"facility":    cfg.Deployment.FacilityID,
			"adapter_id":  cfg.Adapter.ID,
			"environment": cfg.Deployment.Environment,
			"version":     Version,
		},
	}); bootErr != nil {
		// Boot audit failures are loud — if the chain cannot extend on
		// the first row, the durable store is broken.
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("audit boot event: %w", bootErr)
	}

	// --- 5. Auth verifier + token endpoint ------------------------------
	verif, tokenSrv, err := buildAuthEndpoints(cfg.Auth, pool, authClients)
	if err != nil {
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("auth: %w", err)
	}
	rt.authVerif = verif
	rt.tokenSrv = tokenSrv

	// --- 6. Channels: rest-hook (production default) --------------------
	// Build the URL validator up front so the rest-hook channel can
	// re-check the subscriber endpoint at delivery time (OP #182). The
	// API layer at step 7 reuses the same instance for create-time
	// validation, so a hostname's create-time approval and delivery-
	// time re-check share one policy surface.
	urlValidator := handlers.NewURLValidator(handlers.URLValidatorConfig{
		AllowHTTP: cfg.Auth.AllowInsecure, // dev convenience: if insecure JWKS allowed, allow http endpoints too
	})
	rhCh, err := chresthook.New(chresthook.Options{
		UserAgent:      cfg.Channels.RestHook.UserAgent,
		RequestTimeout: cfg.Channels.RestHook.RequestTimeout,
		MaxRetryAfter:  cfg.Channels.RestHook.MaxRetryAfter,
		MinRetryAfter:  cfg.Channels.RestHook.MinRetryAfter,
		Logger:         logger.With("component", "channel.resthook"),
		URLValidator:   urlValidator,
	})
	if err != nil {
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("channel resthook: %w", err)
	}
	rt.rhChannel = rhCh
	chReg := scheduler.NewMapRegistry()
	rt.chReg = chReg
	chReg.Register("rest-hook", rhCh)

	// --- 6a. WebSocket channel (story #101). Token consumer is the
	// existing ws_binding_tokens repo wrapped in a small adapter.
	// Replayer is the production no-op until the past-events store
	// lands; until then a reconnecting subscriber gets bind-success
	// and zero replay frames, which is the same observable behavior
	// today (the replay path is opt-in via LastReceivedEventNumber).
	wsCh, err := chwebsocket.New(chwebsocket.Options{
		Tokens:                   wsTokenAdapter{pool: pool, repo: repos.NewWsBindingTokensRepo()},
		Replayer:                 noopReplayer{},
		Logger:                   logger.With("component", "channel.websocket"),
		PingInterval:             cfg.Channels.WebSocket.PingInterval,
		IdleTimeout:              cfg.Channels.WebSocket.IdleTimeout,
		MaxFrameBytes:            cfg.Channels.WebSocket.MaxFrameBytes,
		OriginPatterns:           cfg.Channels.WebSocket.OriginPatterns,
		BindTimeout:              cfg.Channels.WebSocket.BindTimeout,
		PingWriteTimeout:         cfg.Channels.WebSocket.PingWriteTimeout,
		UpgradeReadHeaderTimeout: cfg.Channels.WebSocket.UpgradeReadHeaderTimeout,
		MaxSessions:              cfg.Channels.WebSocket.MaxSessions,
		MaxSessionsPerClient:     cfg.Channels.WebSocket.MaxSessionsPerClient,
		MaxReplayEvents:          cfg.Channels.WebSocket.MaxReplayEvents,
	})
	if err != nil {
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("channel websocket: %w", err)
	}
	rt.wsChannel = wsCh
	chReg.Register("websocket", wsCh)

	// --- 6b. Email channel (story #102). Wired only when SMTPHost is
	// non-empty so the development binary (no SMTP relay) does not
	// fail-closed at startup. When wired, From + SMTPPort must also
	// be set; the constructor validates the rest.
	if cfg.Channels.Email.SMTPHost != "" {
		emailCh, eerr := chemail.New(buildEmailConfig(cfg.Channels.Email, logger))
		if eerr != nil {
			rt.shutdown(context.Background())
			return nil, fmt.Errorf("channel email: %w", eerr)
		}
		rt.emailChannel = emailCh
		chReg.Register("email", emailCh)
	}

	// --- 6c. Message channel (story #103). Always wired — the channel
	// has no required-at-construction config knobs; ServerEndpoint is
	// optional and falls back to omitting MessageHeader.source.endpoint.
	msgCh, err := chmessage.New(chmessage.Options{
		Logger:              logger.With("component", "channel.message"),
		UserAgent:           cfg.Channels.Message.UserAgent,
		RequestTimeout:      cfg.Channels.Message.RequestTimeout,
		ServerEndpoint:      cfg.Channels.Message.ServerEndpoint,
		MaxIdleConnsPerHost: cfg.Channels.Message.MaxIdleConnsPerHost,
		MaxConnsPerHost:     cfg.Channels.Message.MaxConnsPerHost,
		TLSMinVersion:       cfg.Channels.Message.TLSMinVersion,
	})
	if err != nil {
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("channel message: %w", err)
	}
	rt.messageChannel = msgCh
	chReg.Register("message", msgCh)

	// --- 7. API router (handlers.RegisterRoutes) ------------------------
	// urlValidator is built at step 6 so the rest-hook channel and the
	// API layer share one policy surface (OP #182).
	// rest-hook gets a real activator that POSTs a synthetic FHIR R5
	// handshake bundle to the subscriber endpoint and only flips status
	// to active on a 2xx response (D-2). email gets a real RCPT-TO probe
	// activator that exercises the configured SMTP relay (OP #114
	// scope-reduced after split: WS halves moved to #114b/#114c).
	// websocket continues to use the no-op default — the websocket
	// handshake is asynchronous (the subscriber binds with a token after
	// creation) and the SPI seams for a real handshake do not exist yet
	// (#114b adds them, #114c uses them). message stays on the default
	// pending its own activation story.
	rhActivator := newRestHookActivator(restHookActivatorOptions{
		AllowHTTP: cfg.Auth.AllowInsecure,
		Timeout:   cfg.Channels.RestHook.RequestTimeout,
		Logger:    logger.With("component", "channel.resthook.activator"),
	})
	rt.rhActivator = rhActivator
	channels := handlers.ChannelRegistry{
		"rest-hook": rhActivator,
		"websocket": defaultActivator{},
		"message":   defaultActivator{},
	}
	// Email activator: real RCPT-TO probe when SMTP is configured;
	// fail-closed activator when it isn't. Critically, NEVER
	// defaultActivator{} for email — the no-fakes rule is strict
	// (epic #91 / OP #114) and a dev binary that accepts email
	// subscriptions without an SMTP relay must not lie about
	// activation. The fail-closed activator returns HandshakeFailed
	// with a reason the operator can see in the audit log.
	if rt.emailChannel != nil {
		channels["email"] = newEmailActivator(rt.emailChannel)
	} else {
		channels["email"] = unconfiguredEmailActivator{}
	}
	// Auth middleware: in production cfg.Auth.Audience is required and
	// Validate has already enforced it (story #116). The dev-bypass
	// path — verif == nil because cfg.Auth.AllowDevBypass=true — is the
	// ONLY place we install devPrincipalMiddleware; story #117 made
	// this an explicit opt-in so an empty audience field cannot
	// silently install a no-op auth gate that authorizes every caller.
	// N-1.4 still holds — the route group always has SOME middleware
	// bound.
	var authMiddleware handlers.Middleware
	switch {
	case verif != nil:
		authMiddleware = verif.Middleware
	case cfg.Auth.AllowDevBypass:
		authMiddleware = devPrincipalMiddleware()
	default:
		// Validate guarantees we never reach this branch in
		// production. The runtime guard exists so a future regression
		// in Validate fails closed rather than installing a no-op
		// middleware.
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("wiring: auth.audience is empty and auth.allow_dev_bypass is false; refusing to install no-op auth")
	}

	deps := handlers.Deps{
		Auth:                authMiddleware,
		Subscriptions:       handlers.NewPgSubscriptionsStore(pool),
		Topics:              handlers.NewPgTopicsStore(pool),
		Events:              handlers.NewPgEventsStore(pool),
		Deliveries:          handlers.NewPgDeliveriesStore(pool),
		WsTokens:            handlers.NewPgWsBindingTokensStore(pool),
		Audit:               handlers.NewChainedAuditStore(obsMod.AuditWriter()),
		Channels:            channels,
		Metrics:             apiMetrics,
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

	// Story #104 (S-3.3): plug per-client token buckets into the chi
	// middleware on POST /Subscription and on $get-ws-binding-token
	// before RegisterRoutes mounts them. The middleware is nil-safe,
	// so operators who omit the rate-limit blocks keep unbounded
	// behavior — exactly the pattern story #92 used for Admin.
	rateLimits := buildClientRateLimitersFromAuth(&cfg.Auth, nil)
	deps.SubscriptionCreateRateLimit = rateLimits.SubscriptionCreate
	deps.WSBindingTokenRateLimit = rateLimits.WSBindingToken

	r := chi.NewRouter()
	// Mount /metrics on the chi router so Prometheus scrapes against
	// the same HTTP listener as the FHIR API (story #94 AC #3).
	r.Handle("/metrics", obsMod.PrometheusHandler())
	// Story #93 / S-2.1: mount the public routes (today: just
	// /metadata for FHIR conformance probes) BEFORE RegisterRoutes
	// installs the auth-protected group. chi serves the bare-router
	// GET /metadata to unauthenticated callers; the auth-protected
	// FHIR API sits behind the auth middleware on the inner group.
	handlers.RegisterPublicRoutes(r, deps)
	handlers.RegisterRoutes(r, deps)
	if tokenSrv != nil {
		r.Method(http.MethodPost, "/token", tokenSrv)
	}

	// Story #92: validate the admin shared secret meets the minimum
	// length BEFORE we build the supervised pipeline. The router is
	// mounted further down — once the pipeline is up — so the admin
	// surface gets the SupervisorStatus reader (story #99).
	if cfg.Admin.Token != "" && len(cfg.Admin.Token) < handlers.MinAdminTokenBytes {
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("admin: token must be at least %d bytes (got %d)",
			handlers.MinAdminTokenBytes, len(cfg.Admin.Token))
	}
	deps.AdminToken = cfg.Admin.Token
	deps.AdminPathPrefix = cfg.Admin.PathPrefix
	deps.DeadLetters = handlers.NewPgDeadLettersStore(pool, dl)
	deps.AdminRateLimit = auth.NewClientRateLimiter(auth.RateLimit{
		Burst:           cfg.Admin.RateLimit.Burst,
		RefillPerSecond: cfg.Admin.RateLimit.RefillPerSecond,
		MaxKeys:         cfg.Admin.RateLimit.MaxKeys,
	}, nil)
	// Story #100: webhook ingress (vendor-push). Mount the receiver on
	// the SAME chi router that backs the FHIR API so vendors POST
	// signed change events to /webhooks/{adapter}. The receiver is
	// HMAC-only authenticated — the per-adapter shared secret is read
	// fresh on every request from adapter_state(scope='webhook',
	// key='secret') so operators rotate by upserting that row without
	// a restart. The receiver MUST NOT sit behind the bearer
	// middleware: webhook callers do not have OAuth tokens.
	r.Route("/webhooks", func(sub chi.Router) {
		webhook.NewHandler(webhook.Deps{
			Resolver:     newPgWebhookSecretResolver(pool, store.AdapterState()),
			Repo:         rcs,
			Querier:      pool,
			Clock:        func() time.Time { return time.Now().UTC() },
			MaxClockSkew: 5 * time.Minute,
		}).Mount(sub)
	})

	rt.router = r

	// --- 8. MLLP listener (optional) ------------------------------------
	if len(cfg.MLLP.Listeners) > 0 {
		listener, mErr := buildMLLPListener(cfg.MLLP, pool, hl7Q, logger)
		if mErr != nil {
			rt.shutdown(context.Background())
			return nil, fmt.Errorf("mllp: %w", mErr)
		}
		if startErr := listener.Start(ctx); startErr != nil {
			rt.shutdown(context.Background())
			return nil, fmt.Errorf("mllp start: %w", startErr)
		}
		rt.mllpListen = listener
	}

	// --- 9. Pipeline workers --------------------------------------------
	// Story #99: every adapter pipeline worker now runs under a
	// supervisor.Supervisor so a panic in Run is recovered, counted,
	// and the worker is restarted with backoff. The supervisedPipeline
	// also exposes Status snapshots to /admin/supervisor/status.

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
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("hl7processor: %w", err)
	}
	rt.processor = proc

	// Matcher. The CatalogProvider is hot-swappable: at startup we load
	// the operator-supplied topic JSON files (D-1); on SIGHUP the
	// reload handler walks the same directory and Stores a fresh
	// catalog so operators can roll out new topic mappings without a
	// restart.
	rt.topicsDir = cfg.Topics.CatalogDir
	topicSources, err := loadTopicSources(rt.topicsDir)
	if err != nil {
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("topics: load sources: %w", err)
	}
	initialCat, err := catalog.Load(topicSources)
	if err != nil {
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("catalog: %w", err)
	}
	logCatalogDiagnostics(logger, rt.topicsDir, initialCat)
	cp := matcher.NewAtomicCatalogProvider(initialCat.Catalog)
	rt.catalogProv = cp
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

	// FhirScanRunner worker (story #96). Built only when the loaded
	// adapter declares Capabilities.FhirScanRunner=true; the registry
	// already enforces (#65) that a true capability has a non-nil
	// builder, so the BuildFhirScanRunner return is safe to use without
	// a nil check. The worker is registered with the supervisedPipeline
	// alongside the other four workers so it gets the same restart +
	// drain semantics for free.
	var scanWorker *scanrunner.Worker
	if loadedAdapter.Manifest().Capabilities.FhirScanRunner {
		runner := loadedAdapter.BuildFhirScanRunner(adapterspi.AdapterContext{
			AdapterID: cfg.Adapter.ID,
			Now:       time.Now,
		})
		w, err := scanrunner.New(scanrunner.Options{
			AdapterID: cfg.Adapter.ID,
			Runner:    runner,
			Sink:      scanrunner.NewRepoSink(rcs, pool),
			Clock:     time.Now,
		})
		if err != nil {
			rt.shutdown(context.Background())
			return nil, fmt.Errorf("scanrunner: %w", err)
		}
		scanWorker = w
	}

	// VendorAPIClient worker (story #97). Built only when the loaded
	// adapter declares Capabilities.VendorAPIClient=true; the registry
	// already enforces (#65) that a true capability has a non-nil
	// builder, so the BuildVendorAPIClient return is safe to use without
	// a nil check. The worker is registered with the supervisedPipeline
	// alongside the other pipeline workers so it gets the same restart +
	// drain semantics for free.
	var vendorWorker *vendorclient.Worker
	if loadedAdapter.Manifest().Capabilities.VendorAPIClient {
		vc := loadedAdapter.BuildVendorAPIClient(adapterspi.AdapterContext{
			AdapterID: cfg.Adapter.ID,
			Now:       time.Now,
		})
		w, err := vendorclient.New(vendorclient.Options{
			AdapterID: cfg.Adapter.ID,
			Client:    vc,
			Sink:      vendorclient.NewRepoSink(rcs, pool),
			Clock:     time.Now,
		})
		if err != nil {
			rt.shutdown(context.Background())
			return nil, fmt.Errorf("vendorclient: %w", err)
		}
		vendorWorker = w
	}

	// Build the supervised pipeline. This replaces the prior bare
	// `go w.Run(loopCtx)` pattern (story #99): each worker now runs
	// under an internal/adapter/supervisor.Supervisor that recovers
	// panics, restarts on exit with bounded exponential backoff, and
	// exposes a Status snapshot to /admin/supervisor/status.
	supDeps := pipelineSupervisorDeps{
		HL7:        proc,
		Matcher:    matcherWorker,
		Submatcher: submatcherWorker,
		Scheduler:  schedulerWorker,
		Lifecycle:  lcMod,
		Backoff:    cfg.Pipeline.Supervisor,
	}
	// Assign FhirScanRunner only when non-nil; assigning a typed-nil
	// *scanrunner.Worker to an interface field would produce a non-nil
	// interface that buildSupervisedPipeline would mistakenly host.
	if scanWorker != nil {
		supDeps.FhirScanRunner = scanWorker
	}
	// Same typed-nil guard for VendorAPIClient (story #97).
	if vendorWorker != nil {
		supDeps.VendorAPIClient = vendorWorker
	}
	pipeline, perr := buildSupervisedPipeline(supDeps)
	if perr != nil {
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("pipeline supervisor: %w", perr)
	}
	rt.pipeline = pipeline
	deps.SupervisorStatus = pipeline

	// Mount the admin surface now that the supervisor reader is wired
	// (story #92 + story #99). Token-length validation happened above;
	// here we always call RegisterAdminRoutes — it is a no-op when the
	// token is empty.
	handlers.RegisterAdminRoutes(r, deps)

	// --- 10. Lifecycle shutdown wiring ----------------------------------
	rt.registerLifecycle(lcMod, cfg.Lifecycle.ShutdownGracePeriod)

	// --- 11. Lifecycle readiness check (DB ping) ------------------------
	lcMod.RegisterReadiness("database", func(ctx context.Context) error {
		pingCtx, c := context.WithTimeout(ctx, 2*time.Second)
		defer c()
		return pool.Ping(pingCtx)
	})

	// --- 12. SIGHUP-driven topic catalog reload (D-1) -------------------
	// A signal handler chains: if a future caller (config module) also
	// wants SIGHUP, this becomes a multi-handler dispatch; today the
	// catalog is the only consumer.
	lcMod.SetReloadHandler(func(rctx context.Context) {
		newSources, srcErr := loadTopicSources(rt.topicsDir)
		if srcErr != nil {
			logger.Warn("topic reload: source walk failed; keeping previous catalog",
				"err", srcErr.Error(),
				"dir", rt.topicsDir,
			)
			return
		}
		_ = rctx // catalog.Load is in-process and bounded; ctx is reserved for future
		newCat, loadErr := catalog.Load(newSources)
		if loadErr != nil {
			logger.Warn("topic reload: catalog.Load failed; keeping previous catalog",
				"err", loadErr.Error(),
			)
			return
		}
		logCatalogDiagnostics(logger, rt.topicsDir, newCat)
		rt.catalogProv.Store(newCat.Catalog)
		logger.Info("topic catalog reloaded",
			"dir", rt.topicsDir,
			"topics", len(newCat.Catalog.All()),
			"rejected", len(newCat.Rejected),
		)
	})

	return rt, nil
}

// logCatalogDiagnostics emits a single startup/reload line summarizing
// what the catalog now contains and one line per rejected/overridden
// candidate so operators see exactly which topic JSON file failed.
func logCatalogDiagnostics(logger *slog.Logger, dir string, report catalog.Report) {
	if report.Catalog == nil {
		logger.Warn("topic catalog: nil after Load (treating as empty)")
		return
	}
	logger.Info("topic catalog loaded",
		"dir", dir,
		"topics", len(report.Catalog.All()),
		"rejected", len(report.Rejected),
		"overridden", len(report.Overridden),
	)
	for _, rej := range report.Rejected {
		logger.Warn("topic rejected",
			"origin", rej.Origin,
			"url", rej.URL,
			"reason", rej.Reason,
		)
	}
	for _, ov := range report.Overridden {
		logger.Warn("topic override fallback", "fields", ov.LogFields())
	}
}

// registerLifecycle wires the runtime's components into the lifecycle
// module's shutdown sequencer:
//
//   - PhaseStopAccepting: MLLP listener Close, scheduler stop-accepting
//     (HTTP listener Close is registered from run.go since it owns the
//     *http.Server). Phase 2 stops accepting new work.
//   - PhaseDrainInFlight: pipeline workers drain, in-flight activations
//     drain, storage runners drain (Phase 3, 70% budget).
//   - PhaseCloseConnections: observability shutdown, channels close,
//     rest-hook activator close, database pool close last (Phase 4).
//
// The grace argument is currently advisory — the lifecycle module
// owns the per-phase budget — but is kept on the API so a future
// switch to per-component grace tuning is non-breaking.
func (r *productionRuntime) registerLifecycle(lcMod *lifecycle.LifecycleModule, grace time.Duration) {
	_ = grace

	if r.mllpListen != nil {
		lcMod.RegisterShutdown(lifecycle.ShutdownHook{
			Name:  "mllp.stop_accepting",
			Phase: lifecycle.PhaseStopAccepting,
			Run: func(ctx context.Context) error {
				return r.mllpListen.Shutdown(ctx)
			},
		})
	}

	// Story #207: http.listener.stop_accepting drives the public HTTP
	// server's stop-accepting transition under the per-phase budget.
	// The closure reads r.httpServer lazily because run.go constructs
	// the *http.Server AFTER buildProductionRuntime returns; setHTTPServer
	// publishes it before the sequencer can fire (the only path to
	// shutdown is RequestShutdown, which run.go calls after the server
	// is registered).
	lcMod.RegisterShutdown(lifecycle.ShutdownHook{
		Name:  "http.listener.stop_accepting",
		Phase: lifecycle.PhaseStopAccepting,
		Run: func(ctx context.Context) error {
			srv := r.getHTTPServer()
			if srv == nil {
				return nil
			}
			return srv.Shutdown(ctx)
		},
	})

	// Story #207: scheduler.stop_accepting flips the scheduler's claim
	// loop into "no new work" mode. In-flight dispatches keep running
	// and are awaited by pipeline.supervisors.drain in
	// PhaseDrainInFlight. The hook is non-blocking; the sequencer
	// completes Phase 2 immediately while the scheduler idles.
	if r.scheduler != nil {
		lcMod.RegisterShutdown(lifecycle.ShutdownHook{
			Name:  "scheduler.stop_accepting",
			Phase: lifecycle.PhaseStopAccepting,
			Run: func(_ context.Context) error {
				r.scheduler.StopAccepting()
				return nil
			},
		})
	}

	// The pipeline drain hook is registered inside buildSupervisedPipeline
	// as `pipeline.supervisors.drain` so the supervisor framework owns
	// the cancellation contract end-to-end (story #99).

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

	if r.obsModule != nil {
		// Register the observability shutdown FIRST so the registry
		// holds it before "database.close". Within a phase the
		// sequencer fans the hooks out concurrently — both run together
		// — but registering early keeps the ordering deterministic for
		// any future caller that switches to sequential execution
		// (story #94 AC #6).
		lcMod.RegisterShutdown(lifecycle.ShutdownHook{
			Name:  "observability.shutdown",
			Phase: lifecycle.PhaseCloseConnections,
			Run: func(ctx context.Context) error {
				return r.obsModule.Shutdown(ctx)
			},
		})
	}

	// channels.close drains websocket sessions, rest-hook / message
	// HTTP transports, and the email no-op. Registered in
	// PhaseCloseConnections so in-flight Deliver calls (drained by
	// pipeline.drain in the prior phase) have already returned before
	// transports get torn down (stories #101/#102/#103).
	lcMod.RegisterShutdown(lifecycle.ShutdownHook{
		Name:  "channels.close",
		Phase: lifecycle.PhaseCloseConnections,
		Run: func(_ context.Context) error {
			var errs []error
			if r.chReg != nil {
				for _, code := range []string{"rest-hook", "websocket", "email", "message"} {
					ch, ok := r.chReg.Lookup(code)
					if !ok || ch == nil {
						continue
					}
					if cerr := ch.Close(); cerr != nil {
						errs = append(errs, fmt.Errorf("close %s: %w", code, cerr))
					}
				}
			}
			return errors.Join(errs...)
		},
	})

	// storage.drain stops the partition maintainer + retention sweeper
	// goroutines so they don't block the database close in the next
	// phase. Storage owns those runners; calling Storage.Shutdown is the
	// canonical way to drain them. Storage.Shutdown also closes the
	// underlying pool, but it is idempotent — the database.pool.close
	// hook in PhaseCloseConnections runs Pool.Close again as a no-op so
	// the operator-visible phase contract (story #207) holds even when
	// storage.drain has already done the work.
	lcMod.RegisterShutdown(lifecycle.ShutdownHook{
		Name:  "storage.drain",
		Phase: lifecycle.PhaseDrainInFlight,
		Run: func(ctx context.Context) error {
			if r.storage == nil {
				return nil
			}
			return r.storage.Shutdown(ctx)
		},
	})

	// Story #207: explicit database.pool.close hook in
	// PhaseCloseConnections. storage.drain in the prior phase already
	// closes the pool as a side effect (Storage.Shutdown wraps
	// pool.Close); pool.Close is idempotent so a second call is a
	// no-op. Registering the hook nonetheless gives operators the
	// per-phase metric line (`fhir_subs_lifecycle_phase_duration_seconds
	// {phase="close_connections"}` non-zero) and pins the contract that
	// connection-tier teardown lives in Phase 4.
	lcMod.RegisterShutdown(lifecycle.ShutdownHook{
		Name:  "database.pool.close",
		Phase: lifecycle.PhaseCloseConnections,
		Run: func(_ context.Context) error {
			if r.pool == nil {
				return nil
			}
			// pgxpool.Pool.Close is synchronous and waits for in-flight
			// queries; storage.drain already drained those. Bound it
			// loosely via a goroutine so a stuck connection cannot pin
			// the phase past its budget — pgxpool's own teardown takes
			// over once the goroutine returns.
			done := make(chan struct{})
			go func() {
				r.pool.Close()
				close(done)
			}()
			select {
			case <-done:
				return nil
			case <-time.After(2 * time.Second):
				return nil // phase deadline owns the budget
			}
		},
	})

	// Story #207: rest-hook activator transport close. The activator's
	// http.Transport keeps idle TCP/TLS sockets to subscriber endpoints
	// for keep-alive reuse; PhaseCloseConnections releases them so the
	// process exits without warm sockets.
	if r.rhActivator != nil {
		lcMod.RegisterShutdown(lifecycle.ShutdownHook{
			Name:  "resthook.activator.close",
			Phase: lifecycle.PhaseCloseConnections,
			Run: func(_ context.Context) error {
				return r.rhActivator.Close()
			},
		})
	}
}

// setHTTPServer publishes the public HTTP server so the
// PhaseStopAccepting hook (registered in registerLifecycle) can call
// srv.Shutdown when the lifecycle sequencer fires. Idempotent — runs
// once during startup before MarkStartupComplete (story #207).
func (r *productionRuntime) setHTTPServer(srv *http.Server) {
	if r == nil {
		return
	}
	r.httpServerMu.Lock()
	r.httpServer = srv
	r.httpServerMu.Unlock()
}

// getHTTPServer returns the registered HTTP server or nil. The
// PhaseStopAccepting hook closure reads via this method so the
// lock-protected read survives the data-race detector when run.go
// publishes the server from the main goroutine and the sequencer
// invokes the hook from its own goroutine.
func (r *productionRuntime) getHTTPServer() *http.Server {
	if r == nil {
		return nil
	}
	r.httpServerMu.Lock()
	srv := r.httpServer
	r.httpServerMu.Unlock()
	return srv
}

// shutdown performs an immediate teardown for the buildProductionRuntime
// failure paths. Once the runtime has been registered with the lifecycle
// module, shutdown is driven by the sequencer and this method is not
// called.
func (r *productionRuntime) shutdown(ctx context.Context) {
	if r == nil {
		return
	}
	if r.pipeline != nil {
		_ = r.pipeline.Stop(ctx)
	}
	if r.mllpListen != nil {
		_ = r.mllpListen.Shutdown(ctx)
	}
	if r.storage != nil {
		// Storage.Shutdown owns the partition + retention drain AND the
		// pool close. Bound by ctx so a stuck dial inside pgxpool can't
		// pin the failure path; storage's own internal budget continues
		// in the background.
		_ = r.storage.Shutdown(ctx)
		return
	}
	if r.pool != nil {
		closed := make(chan struct{})
		go func() {
			r.pool.Close()
			close(closed)
		}()
		select {
		case <-closed:
		case <-time.After(3 * time.Second):
		}
	}
}

// decodeCodecKeys validates and base64-decodes the YAML codec key
// bundle into the version->bytes map storage.Config.KeyVersions
// expects. Story #95: storage.Start owns the codec construction now,
// so this helper is shared between the wiring path (storage.Start) and
// the audit-CLI path (which still builds a freestanding codec).
func decodeCodecKeys(cfg CodecConfig) (map[int32][]byte, error) {
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
	return keys, nil
}

// buildStorageConfig translates the cmd-side YAML config into the
// storage.Config bundle storage.Start consumes. Operator-supplied
// storage.retention.* and storage.partitioning.* values pass through;
// zero values fall back to storage.Config.ApplyDefaults inside
// storage.Start.
func buildStorageConfig(cfg *Config, keys map[int32][]byte) storage.Config {
	return storage.Config{
		PostgresURL: cfg.Database.URL,
		Pool: pool.Config{
			ApplicationName: "fhir-ehr-subscriptions-service",
		},
		KeyVersions: keys,
		ActiveKey:   cfg.Codec.ActiveKeyVersion,
		Retention: storage.RetentionConfig{
			Hl7MessageQueue: cfg.Storage.Retention.Hl7MessageQueue,
			Deliveries:      cfg.Storage.Retention.Deliveries,
			DeadLetters:     cfg.Storage.Retention.DeadLetters,
			AuditLog:        cfg.Storage.Retention.AuditLog,
			RunInterval:     cfg.Storage.Retention.RunInterval,
			BatchSize:       cfg.Storage.Retention.BatchSize,
			BatchPause:      cfg.Storage.Retention.BatchPause,
			TickTimeout:     cfg.Storage.Retention.TickTimeout,
		},
		Partitioning: storage.PartitionConfig{
			AutoDrop:                 cfg.Storage.Partitioning.AutoDrop,
			PartitionLockTimeout:     cfg.Storage.Partitioning.PartitionLockTimeout,
			RunInterval:              cfg.Storage.Partitioning.RunInterval,
			TickTimeout:              cfg.Storage.Partitioning.TickTimeout,
			ResourceChangesRetention: cfg.Storage.Partitioning.ResourceChangesRetention,
			EhrEventsRetention:       cfg.Storage.Partitioning.EhrEventsRetention,
		},
		Lifecycle: storage.LifecycleConfig{
			ShutdownGracePeriod: cfg.Lifecycle.ShutdownGracePeriod,
		},
	}
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

// nonZeroInt32 returns v if v > 0, else fallback.
func nonZeroInt32(v, fallback int32) int32 {
	if v > 0 {
		return v
	}
	return fallback
}

// silence the unused-import diagnostic emitted while the package
// scaffolding is still being written. The reference here goes away
// once the e2e harness exercises every component.
var _ channel.Channel = channel.Channel(nil)

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

// devPrincipalMiddleware reads the X-Client-Id header and, when
// present, attaches a Principal to the request context with a
// permissive scope set covering every operator-mintable scope used by
// the API. The scope set mirrors the auth_clients.scopes column so a
// dev request behaves exactly like an authenticated client whose
// scopes were granted out-of-band.
//
// This is wired ONLY when cfg.Auth.Audience is empty (the dev
// /e2e fallback path). Production deployments MUST set audience so the
// real verifier is installed instead. Empty header still produces a
// 401 from mustPrincipal — the dev path does not invent identities.
func devPrincipalMiddleware() handlers.Middleware {
	scopes := []string{
		"system/Subscription.cruds",
		"system/Subscription.r",
		"system/Subscription.s",
		"system/Subscription.c",
		"system/Subscription.u",
		"system/Subscription.d",
		"system/Subscription.$status",
		"system/Subscription.$events",
		"system/Subscription.$get-ws-binding-token",
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cid := r.Header.Get("X-Client-Id")
			if cid == "" {
				next.ServeHTTP(w, r)
				return
			}
			p := &auth.Principal{ClientID: cid, Scopes: scopes}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	}
}

// buildObservabilityConfig translates the operator-facing tracing /
// metrics / audit blocks into the observability package's typed
// shape. The mapping is 1:1 today; the helper exists so future
// non-trivial transforms (e.g. env-var interpolation, default
// elision) have a single owner (story #94 AC #1, #7).
func buildObservabilityConfig(cfg *Config) observability.Config {
	if cfg == nil {
		return observability.Config{}
	}
	return observability.Config{
		Metrics: observability.MetricsConfig{
			Bind: cfg.Metrics.Bind,
			Path: cfg.Metrics.Path,
		},
		Tracing: observability.TracingConfig{
			OTLPEndpoint:    cfg.Tracing.OTLPEndpoint,
			SampleRate:      cfg.Tracing.SampleRate,
			ExporterTimeout: cfg.Tracing.ExporterTimeout,
			Insecure:        cfg.Tracing.Insecure,
			TLSCertFile:     cfg.Tracing.TLS.CertFile,
			TLSKeyFile:      cfg.Tracing.TLS.KeyFile,
			TLSCAFile:       cfg.Tracing.TLS.CAFile,
			Headers:         cfg.Tracing.Headers,
		},
		Logging: observability.LoggingConfig{
			Level:  cfg.Deployment.LogLevel,
			Format: cfg.Deployment.LogFormat,
		},
		Audit: observability.AuditConfig{
			Sink:              cfg.Audit.Sink,
			FilePath:          cfg.Audit.FilePath,
			FileSyncMode:      cfg.Audit.FileSyncMode,
			FileBatchInterval: cfg.Audit.FileBatchInterval,
		},
	}
}
