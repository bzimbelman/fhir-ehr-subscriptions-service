// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/registry"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/scanrunner"
	adapterspi "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/vendorclient"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	apimetrics "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/metrics"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/wsbindingcache"
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

	// deps is the effective handlers.Deps the API router was built
	// with. Stored so wiring tests can assert that Config knobs
	// reached the live Deps without tunnelling through the chi
	// closure (stories #176-#181). Read-only after buildProductionRuntime
	// returns.
	deps handlers.Deps

	// reloadTopicCatalog is the topic-catalog hot-apply hook. The
	// reload coordinator (run.go) invokes it after a successful whole-
	// config reload (story #151). Nil-safe: empty when production
	// mode is not active.
	reloadTopicCatalog func()

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

	// loadedAdapter is the adapter the registry chose for this run.
	// Story #98: tests reach for it via reflection to assert
	// HydrationService wire-up; production code holds the reference
	// here so the lifecycle module can call OnShutdown later if a
	// future adapter uses that hook.
	loadedAdapter adapterspi.EhrAdapter

	// wsTokenCache is the OP #242 in-process per-client cache that
	// fronts $get-ws-binding-token. Stored on the runtime so the
	// lifecycle module can join the background sweeper goroutine
	// during graceful shutdown.
	wsTokenCache       handlers.WsBindingTokenCache
	wsTokenCacheRaw    *wsbindingcache.Cache
	wsTokenSweeperStop context.CancelFunc
	wsTokenSweeperDone <-chan struct{}

	logger *slog.Logger
}

// ChannelRegistry exposes the scheduler-side ChannelRegistry to callers
// that need to inspect or interact with the registered delivery
// channels (e.g. integration tests, lifecycle hooks). Returning the
// interface, not the *MapRegistry, keeps the runtime free to swap the
// implementation later without touching callers. Nil-safe: returns a
// nil interface when the runtime hasn't reached the channels-wired
// stage.
func (r *productionRuntime) ChannelRegistry() scheduler.ChannelRegistry {
	if r == nil || r.chReg == nil {
		return nil
	}
	return r.chReg
}

// buildProductionRuntime constructs the full production stack from cfg.
// It is invoked by runWithHooks when cfg.Database.URL is non-empty. On
// any failure it tears down anything already opened so the caller can
// surface a fatal error without leaking handles.
//
// The function orchestrates per-subsystem builders that live in
// dedicated wiring_*.go files:
//
//   - wiring_storage.go     : decodeCodecKeys, buildStorageConfig
//   - wiring_observability.go : buildObservabilityConfig, logCatalogDiagnostics
//   - wiring_auth.go        : buildAuthEndpoints, devPrincipalMiddleware, defaultActivator
//   - wiring_channels.go    : wsTokenAdapter, noopReplayer, buildEmailConfig
//   - wiring_adapter.go     : buildMLLPListener (+TLS, persister), pgWebhookSecretResolver
//   - wiring_lifecycle.go   : registerLifecycle, setHTTPServer/getHTTPServer, shutdown
//   - wiring_admin.go       : applyAdminDeps (admin token validation + admin Deps fields)
//   - supervised_pipeline.go: buildSupervisedPipeline
//   - adapter_registry.go   : registerAllAdapters
//   - activators.go         : newRestHookActivator, newEmailActivator, unconfiguredEmailActivator
//   - rate_limit_wiring.go  : buildClientRateLimitersFromAuth
//
// Step ordering and error handling are unchanged from before the split
// (OP #285, mechanical refactor).
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
	rt.loadedAdapter = loadedAdapter
	// OP #311/#313: gate the host-side hl7processor on
	// Capabilities.HL7Processor. Direct (OP #175) declares all
	// capabilities false because Direct messaging is SMTP/S-MIME, not
	// HL7 v2 over MLLP — BuildHl7Processor returns nil for that adapter,
	// and feeding nil into hl7processor.New as Deps.Adapter fails with
	// "Deps.Adapter is required". Mirror the gating already used for
	// FhirScanRunner / VendorAPIClient / HydrationService.
	var hl7Proc adapterspi.Hl7MessageProcessor
	if loadedAdapter.Manifest().Capabilities.HL7Processor {
		hl7Proc = loadedAdapter.BuildHl7Processor(adapterspi.AdapterContext{
			AdapterID: cfg.Adapter.ID,
			Now:       time.Now,
		})
	}

	// Story #98: build the adapter HydrationService so the scheduler
	// can expand `_include` / `_revinclude` rules for full-resource
	// notifications. The adapter manifest is the source of truth: we
	// only call BuildHydrationService when the loaded adapter declares
	// Capabilities.HydrationService=true (registry validation in #65
	// already guarantees the builder is non-nil when the capability is
	// declared). Adapters that omit hydration leave the scheduler with
	// a nil service; full-resource bundles fall back to focus-only.
	var hydrationSvc adapterspi.HydrationService
	if loadedAdapter.Manifest().Capabilities.HydrationService {
		hydrationSvc = loadedAdapter.BuildHydrationService(adapterspi.AdapterContext{
			AdapterID:            cfg.Adapter.ID,
			Now:                  time.Now,
			HydrationFhirBaseURL: cfg.Hydration.FhirBaseURL,
		})
	}

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
			"version":     GetVersion(),
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
	//
	// OP #184: AllowHTTP is now sourced from cfg.URLValidator.AllowHTTP
	// (a dedicated url_validator.* block) NOT cfg.Auth.AllowInsecure.
	// The two trust decisions are independent: an operator opting into
	// insecure JWKS for a dev IDP must not implicitly open http:// rest-
	// hook endpoints. Emit a WARN if BOTH switches are flipped on so an
	// operator-visible audit line lands at startup.
	if cfg.Auth.AllowInsecure && cfg.URLValidator.AllowHTTP {
		logger.Warn("BOTH auth.allow_insecure_jwks AND url_validator.allow_http are true; insecure JWKS and plaintext rest-hook endpoints are independently gated and both are now enabled",
			"auth.allow_insecure_jwks", cfg.Auth.AllowInsecure,
			"url_validator.allow_http", cfg.URLValidator.AllowHTTP,
		)
	}
	// OP #185 + legacy compat: AllowHosts is now sourced from
	// cfg.URLValidator.AllowHosts (the new home). The legacy
	// cfg.Auth.AllowSubscriberHosts list is preserved for backward
	// compat — chart values and existing e2e configs still ship it —
	// but operators should migrate to the dedicated url_validator
	// block. Both lists merge.
	allowHosts := append([]string(nil), cfg.URLValidator.AllowHosts...)
	allowHosts = append(allowHosts, cfg.Auth.AllowSubscriberHosts...)
	urlValidator := handlers.NewURLValidator(handlers.URLValidatorConfig{
		AllowHTTP:  cfg.URLValidator.AllowHTTP,
		AllowHosts: allowHosts,
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
	// OP #184: the activator's AllowHTTP shares the URL validator's
	// trust boundary, NOT the auth verifier's. Operators opt into
	// insecure handshakes by flipping url_validator.allow_http, the
	// same switch the create-time + delivery-time SSRF gate honours.
	rhActivator := newRestHookActivator(restHookActivatorOptions{
		AllowHTTP: cfg.URLValidator.AllowHTTP,
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
		// Seed an auth_clients row for each configured dev client id
		// so the subscriptions.client_id FK passes when one of them
		// POSTs a Subscription. Idempotent via ON CONFLICT.
		for _, cid := range cfg.Auth.DevBypassClientIDs {
			cid = strings.TrimSpace(cid)
			if cid == "" {
				continue
			}
			if _, serr := pool.Exec(ctx, `
				INSERT INTO auth_clients (id, scopes, display_name)
				VALUES ($1, ARRAY['system/Subscription.cruds']::text[], $1)
				ON CONFLICT (id) DO NOTHING
			`, cid); serr != nil {
				rt.shutdown(context.Background())
				return nil, fmt.Errorf("seed dev-bypass auth_clients %q: %w", cid, serr)
			}
		}
	default:
		// Validate guarantees we never reach this branch in
		// production. The runtime guard exists so a future regression
		// in Validate fails closed rather than installing a no-op
		// middleware.
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("wiring: auth.audience is empty and auth.allow_dev_bypass is false; refusing to install no-op auth")
	}

	baseURL, wsBaseURL := derivePublicURLs(cfg)
	// OP #242: per-client ws-binding-token cache + 30s sweeper.
	// MaxKeys defaults to 65536 entries; the sweeper goroutine joins
	// shutdown via the lifecycle module.
	wsCacheMax := cfg.Auth.WSBindingTokenCacheMaxKeys
	if wsCacheMax <= 0 {
		wsCacheMax = 65536
	}
	wsCache := wsbindingcache.New(wsbindingcache.Options{
		MaxKeys: wsCacheMax,
		Now:     func() time.Time { return time.Now().UTC() },
	})
	rt.wsTokenCacheRaw = wsCache
	rt.wsTokenCache = handlers.WrapWsBindingTokenCache(wsCache)
	sweeperCtx, sweeperCancel := context.WithCancel(ctx)
	rt.wsTokenSweeperStop = sweeperCancel
	rt.wsTokenSweeperDone = wsbindingcache.StartSweeper(sweeperCtx, wsCache, 30*time.Second, nil)

	wsBindingTTL := cfg.API.WSBindingTTL
	if wsBindingTTL == 0 {
		wsBindingTTL = 5 * time.Minute
	}
	// OP #188: plumb the page-size cap from cfg.Handlers into the
	// PgSubscriptionsStore so a high-fan-out tenant cannot pull a
	// multi-thousand-row resultset in one ListByClient call.
	subsStore := handlers.NewPgSubscriptionsStore(pool).
		WithMaxListByClientPageSize(cfg.Handlers.MaxListByClientPageSize)
	deps := handlers.Deps{
		Auth:          authMiddleware,
		Subscriptions: subsStore,
		Topics:        handlers.NewPgTopicsStore(pool),
		Events:        handlers.NewPgEventsStore(pool),
		Deliveries:    handlers.NewPgDeliveriesStore(pool),
		WsTokens:      handlers.NewPgWsBindingTokensStore(pool),
		WsTokenCache:  rt.wsTokenCache,
		Audit:         handlers.NewChainedAuditStore(obsMod.AuditWriter()),
		Channels:      channels,
		Metrics:       apiMetrics,
		// Logger threads through to handlers' activation-side error
		// path so failures the API can't surface to the client land in
		// structured logs instead of being silently dropped (story #177).
		Logger:              logger.With("component", "api"),
		Now:                 func() time.Time { return time.Now().UTC() },
		WSBindingTTL:        wsBindingTTL,
		BaseURL:             baseURL,
		WSBaseURL:           wsBaseURL,
		ServerVersion:       GetVersion(),
		URLValidator:        urlValidator,
		LifecycleCtx:        ctx,
		ActivationTimeout:   30 * time.Second,
		ActivationWaitGroup: &rt.activationWG,
		// API tunables (story #178). Zero values fall back to the
		// handlers package defaults via RegisterRoutes' applyDefaults,
		// so operators who omit api.* keep legacy behavior.
		SearchPageSize:        cfg.API.SearchPageSize,
		SearchMaxPageSize:     cfg.API.SearchMaxPageSize,
		EventReplayPageSize:   cfg.API.EventReplayPageSize,
		MaxStatusBulkIDs:      cfg.API.MaxStatusBulkIDs,
		MaxBodyBytes:          cfg.API.MaxBodyBytes,
		MaxSchemaErrorBytes:   cfg.API.MaxSchemaErrorBytes,
		AuditMaxBytes:         cfg.API.AuditMaxBytes,
		FHIRVersion:           cfg.API.FHIRVersion,
		SupportedFHIRVersions: cfg.API.SupportedFHIRVersions,
		// SMART security (stories #178/#181). JWKSURL falls back to
		// {BaseURL}/.well-known/jwks.json so the JWKS document we
		// serve below is also the value rendered into
		// CapabilityStatement.
		JWKSURL: deriveJWKSURL(cfg.API.JWKSURL, baseURL),
	}
	if tokenSrv != nil && cfg.Auth.TokenURL != "" {
		// Render the operator-supplied token endpoint into the SMART
		// security extension (P1.7). Empty TokenURL leaves the
		// extension absent, matching legacy behavior.
		deps.TokenEndpointURL = cfg.Auth.TokenURL
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
	// Story #181: mount /.well-known/jwks.json on the public mux. The
	// SMART security extension advertises this URL; clients that
	// resolve it MUST get a JSON document with a top-level "keys"
	// array. With HS256 signing the array is empty (the secret is
	// not publishable); the path itself is the contract.
	r.Method(http.MethodGet, "/.well-known/jwks.json", newJWKSHandler())
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
	// length BEFORE we build the supervised pipeline. Admin field
	// assignment + token validation lives in wiring_admin.go so admin
	// concerns are owned in a single file (OP #285 split).
	if admErr := applyAdminDeps(&deps, cfg.Admin, pool, dl); admErr != nil {
		rt.shutdown(context.Background())
		return nil, admErr
	}
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
	rt.deps = deps

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

	// HL7 processor. OP #311/#313: only construct when the adapter
	// declares Capabilities.HL7Processor; hl7Proc is nil for "direct"
	// (and any future SMTP/S-MIME-only adapter) so hl7processor.New
	// would otherwise fail with "Deps.Adapter is required" at boot.
	var proc *hl7processor.Processor
	if hl7Proc != nil {
		processorPoll := cfg.Pipeline.HL7Processor.IdlePollInterval
		if processorPoll == 0 {
			processorPoll = 200 * time.Millisecond
		}
		correlHold := cfg.Pipeline.CorrelationHoldWindow
		if correlHold == 0 {
			correlHold = 30 * time.Second
		}
		p, perr := hl7processor.New(hl7processor.Config{
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
		if perr != nil {
			rt.shutdown(context.Background())
			return nil, fmt.Errorf("hl7processor: %w", perr)
		}
		proc = p
		rt.processor = proc
	}

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
	// OP #154: persist every loaded catalog topic to subscription_topics
	// so the API's createSubscription handler (which queries the DB via
	// PgTopicsStore.ListActive) can find them.  Without this, the
	// matcher sees the topic in memory but POST /Subscription returns
	// 422 "topic not in catalog" — exactly the gap the demo
	// walkthrough hit.  Idempotent: existing rows for (url, version)
	// are skipped.
	if perr := persistCatalogTopics(ctx, pool, repos.NewSubscriptionTopicsRepo(), initialCat.Catalog, logger); perr != nil {
		rt.shutdown(context.Background())
		return nil, fmt.Errorf("topics persist: %w", perr)
	}
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
	// OP #272: the matcher fans ehr_events out per (topic ×
	// subscription.client_id). Without the SubscriptionsRepo it would
	// error on every match.
	matcherWorker.SetSubscriptionsRepo(subsRepo)
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
	// Story #98: thread the loaded adapter's HydrationService and a
	// topic-lookup closure into the scheduler so full-resource
	// subscriptions get `_include` / `_revinclude` expansion at
	// dispatch time. The closure is hot — it reads through
	// catalogProv so a SIGHUP catalog reload (the catalog reload
	// handler is registered below at step 12) propagates into the
	// scheduler without a restart.
	topicLookup := scheduler.TopicLookup(func(url string) (*catalog.Topic, bool) {
		cat := cp.Get()
		if cat == nil {
			return nil, false
		}
		t := cat.Get(url)
		if t == nil {
			return nil, false
		}
		return t, true
	})
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
			// Story #199: surface the operator-tunable knobs.
			// Zero values fall through to scheduler.applyDefaults
			// (30s recovery, 5m stuck, 1 dispatch concurrency) so
			// configs that omit pipeline.scheduler.* keep legacy
			// behavior.
			RecoveryInterval:    cfg.Pipeline.Scheduler.RecoveryInterval,
			StuckThreshold:      cfg.Pipeline.Scheduler.StuckThreshold,
			DispatchConcurrency: cfg.Pipeline.Scheduler.DispatchConcurrency,
		},
		scheduler.Options{
			Logger:           logger.With("component", "scheduler"),
			HydrationService: hydrationSvc,
			Topics:           topicLookup,
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
		Matcher:    matcherWorker,
		Submatcher: submatcherWorker,
		Scheduler:  schedulerWorker,
		Lifecycle:  lcMod,
		Backoff:    cfg.Pipeline.Supervisor,
	}
	// OP #311/#313: HL7 is gated on adapter Capabilities.HL7Processor.
	// Same typed-nil guard as FhirScanRunner / VendorAPIClient: assigning
	// a (*hl7processor.Processor)(nil) to a supervisor.Worker interface
	// field would yield a non-nil interface that the supervisor would
	// mistakenly host (and immediately panic on Run).
	if proc != nil {
		supDeps.HL7 = proc
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

	// --- 12. Topic-catalog reload registration -------------------------
	// The actual SIGHUP signal is dispatched by the reload coordinator
	// (run.go), which calls our hot-apply hook after re-reading the
	// whole config. Registering here keeps the catalog reload colocated
	// with the catalog wiring (D-1) without owning the SIGHUP seam
	// itself (story #151).
	rt.reloadTopicCatalog = func() {
		newSources, srcErr := loadTopicSources(rt.topicsDir)
		if srcErr != nil {
			logger.Warn("topic reload: source walk failed; keeping previous catalog",
				"err", srcErr.Error(),
				"dir", rt.topicsDir,
			)
			return
		}
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
	}

	return rt, nil
}

// persistCatalogTopics inserts each topic the catalog loaded from
// catalog_dir into subscription_topics so the API's createSubscription
// handler — which queries PgTopicsStore.ListActive against the DB —
// finds them. Without this, a config-mounted topic directory is
// matcher-visible but API-invisible: the bridge accepts MLLP messages
// and produces resource_changes, but POST /Subscription returns 422
// "topic not in catalog" because the API never sees a row.
//
// Idempotent: a topic whose (url, version) is already present in the
// table is skipped (no error). The body column is the raw JSON bytes
// from disk; compiled_form is the same bytes today (the matcher
// re-compiles from the JSON on every reload anyway, and storing the
// compiled form is a future optimization tracked separately).
func persistCatalogTopics(ctx context.Context, pool *pgxpool.Pool, repo *repos.SubscriptionTopicsRepo, cat *catalog.Catalog, logger *slog.Logger) error {
	if cat == nil {
		return nil
	}
	inserted := 0
	skipped := 0
	for _, t := range cat.All() {
		existing, err := repo.GetByURLVersion(ctx, pool, t.CanonicalURL, t.Version)
		if err != nil {
			return fmt.Errorf("lookup %s@%s: %w", t.CanonicalURL, t.Version, err)
		}
		if existing != nil {
			skipped++
			continue
		}
		row := repos.SubscriptionTopicRow{
			URL:          t.CanonicalURL,
			Version:      t.Version,
			Title:        t.Title,
			Status:       t.Status,
			Source:       string(t.Source),
			Body:         t.RawJSON,
			CompiledForm: t.RawJSON,
		}
		if _, err := repo.Insert(ctx, pool, row); err != nil {
			return fmt.Errorf("insert %s@%s: %w", t.CanonicalURL, t.Version, err)
		}
		inserted++
	}
	logger.Info("topic catalog persisted to db", "inserted", inserted, "skipped_existing", skipped)
	return nil
}

// nonZeroInt32 returns v if v > 0, else fallback.
func nonZeroInt32(v, fallback int32) int32 {
	if v > 0 {
		return v
	}
	return fallback
}
