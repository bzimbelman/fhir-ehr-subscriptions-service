// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/lifecycle"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/logging"
)

// runHooks lets tests observe internal state transitions deterministically. In
// production the hooks are nil. The hooks are intentionally minimal: enough to
// drive the shutdown-window readiness assertion without freezing test time.
//
// The forceClose / shutdownErr hooks live on the struct rather than as
// package-level globals so parallel tests in the same package don't race
// each other reading and writing them.
type runHooks struct {
	// onListening fires after the HTTP server is bound and accepting traffic.
	// addr is the actual bound address (matters when bind uses :0).
	onListening func(addr string)
	// onProbeListening fires after the probe listener (S-118) is bound.
	// probeAddr is the actual bound address (matters when probe_bind
	// uses :0). Tests that want to assert /healthz, /readyz, /startup
	// behavior must hit probeAddr — probes no longer live on the main
	// auth-protected mux.
	onProbeListening func(probeAddr string)
	// onShutdownStart fires after the registry is marked shutting_down but
	// before the HTTP server begins draining.
	onShutdownStart func(reg *lifecycleRegistry)
	// onStartupComplete fires after lifecycle.MarkStartupComplete has been
	// called — the gate that flips /healthz from "starting" to "ok" and
	// readyz to walking the registered checks. Audit B-3.
	onStartupComplete func()
	// onServerConfigured fires once the *http.Server has been built but
	// before Serve runs. Tests use this to assert the production timeouts
	// match the audit's B-2 requirements.
	onServerConfigured func(s *http.Server)
	// onLoggerReady fires once the observability logger has been
	// constructed. Tests use this to emit a probe record and assert the
	// PHI-redacting handler is in the chain (S-1.4).
	onLoggerReady func(lg *slog.Logger)
	// forceClose, when non-nil, replaces srv.Close in the grace-exhaustion
	// path so tests can assert the close error is logged (S-1.5).
	forceClose func() error
	// shutdownErr, when non-nil, replaces srv.Shutdown's return value so
	// the grace-exhaustion / Close branch is exercised deterministically
	// without timing tricks (S-1.5).
	shutdownErr func() error
}

// run is the production entry point used by main(). It is a thin wrapper
// around runWithHooks with no test hooks installed.
func run(ctx context.Context, cfg *Config, logOut io.Writer) error {
	return runWithHooks(ctx, cfg, logOut, runHooks{})
}

// runWithHooks owns the full process lifetime for one boot:
//   - validates the config
//   - constructs the lifecycle module (probe handlers, shutdown sequencer)
//   - binds and serves the HTTP listener with audited timeouts
//   - waits on ctx.Done() (the signal handler cancels ctx)
//   - drives graceful shutdown bounded by lifecycle.shutdown_grace_period
//
// The function returns nil on a clean graceful shutdown and a non-nil error
// for any startup or shutdown failure.
func runWithHooks(ctx context.Context, cfg *Config, logOut io.Writer, hooks runHooks) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	cfg.Server.HTTP.applyTimeoutDefaults()

	// Use the observability/logging redacting handler so PHI fields are
	// scrubbed at info+ regardless of caller carelessness (S-1.4). JSON
	// is the documented production default (container log shippers
	// expect it); operators who want human-readable text in local dev
	// set deployment.log_format: text (story #160).
	//
	// Story #151: the level is fed via *slog.LevelVar so a
	// SIGHUP-driven config reload can swap the threshold live without
	// reconstructing the handler (which would lose every per-handler
	// attribute attached upstream).
	levelVar := new(slog.LevelVar)
	levelVar.Set(slogLevel(cfg.Deployment.LogLevel))
	logger := logging.NewLogger(&logging.Options{
		Sink:     logOut,
		LevelVar: levelVar,
		Format:   logFormatOrDefault(cfg.Deployment.LogFormat),
	})
	if hooks.onLoggerReady != nil {
		hooks.onLoggerReady(logger)
	}
	logger.Info(banner(cfg.Deployment.FacilityID, cfg.Adapter.ID),
		"facility_id", cfg.Deployment.FacilityID,
		"adapter_id", cfg.Adapter.ID,
		"version", GetVersion(),
		"commit", GetCommit(),
		"environment", cfg.Deployment.Environment,
	)

	// Lifecycle module owns probe aggregation, shutdown sequencing, and
	// signal dispatch. Audit B-1 / B-3.
	lcMod, err := lifecycle.Start(ctx, lifecycle.LifecycleConfig{
		ShutdownGracePeriod: cfg.Lifecycle.ShutdownGracePeriod,
	}, lifecycle.LifecycleContext{
		Logger: logger,
	})
	if err != nil {
		return fmt.Errorf("lifecycle: start: %w", err)
	}
	// Bridge the legacy lifecycleRegistry surface (still used by the
	// onShutdownStart test hook) to the lifecycle module's flags.
	reg := newLifecycleRegistry()

	// B-4: when the operator opted into production mode AND configured
	// a database URL, construct the full production runtime: DB pool +
	// migrations, codec, channel registry, adapter, auth verifier,
	// handlers.RegisterRoutes, MLLP listener, pipeline workers. Every
	// failure here is fatal — listener never binds, /healthz never
	// flips to ok, the binary exits non-zero with a clear error.
	//
	// Story #117: probe-only mode is the explicit opt-in for
	// running without the production runtime; a missing database URL
	// no longer silently downgrades the deployment. The integration
	// tests that exercise specific subsystems set mode=probe-only
	// alongside a real DB — the runtime still builds in that case.
	var prod *productionRuntime
	if cfg.Deployment.Mode == DeploymentModeProduction || cfg.Database.URL != "" {
		var rtErr error
		prod, rtErr = buildProductionRuntime(ctx, cfg, logger, lcMod)
		if rtErr != nil {
			_ = lcMod.WaitForExit(context.Background())
			return fmt.Errorf("production wiring: %w", rtErr)
		}
	}

	// Pre-bind the listener so we know the chosen port before starting the
	// server goroutine. Lets tests use bind=":0" without a race.
	listener, err := net.Listen("tcp", cfg.Server.HTTP.Bind)
	if err != nil {
		if prod != nil {
			prod.shutdown(context.Background())
		}
		return fmt.Errorf("listen %s: %w", cfg.Server.HTTP.Bind, err)
	}
	addr := listener.Addr().String()

	// Probe listener (S-118). Bound separately so /healthz, /readyz,
	// /startup are reachable on a port that is NEVER wrapped in auth
	// middleware — a buggy auth config can't 401 a kubelet probe and
	// leave pods un-Ready forever. The helm chart's
	// `port: probes -> 8081` lands on this socket.
	probeListener, err := net.Listen("tcp", cfg.Server.HTTP.ProbeBind)
	if err != nil {
		_ = listener.Close()
		if prod != nil {
			prod.shutdown(context.Background())
		}
		return fmt.Errorf("listen probe %s: %w", cfg.Server.HTTP.ProbeBind, err)
	}
	probeAddr := probeListener.Addr().String()
	probeMux := buildProbeMux(lcMod)
	probeSrv := &http.Server{
		Handler:           probeMux,
		ReadHeaderTimeout: cfg.Server.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.Server.HTTP.ReadTimeout,
		WriteTimeout:      cfg.Server.HTTP.WriteTimeout,
		IdleTimeout:       cfg.Server.HTTP.IdleTimeout,
		MaxHeaderBytes:    cfg.Server.HTTP.MaxHeaderBytes,
	}
	probeServeErr := make(chan error, 1)
	go func() {
		err := probeSrv.Serve(probeListener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			probeServeErr <- err
			return
		}
		probeServeErr <- nil
	}()
	logger.Info("probe listener listening", "addr", probeAddr)
	if hooks.onProbeListening != nil {
		hooks.onProbeListening(probeAddr)
	}

	// OP #338: register the probe listener's graceful shutdown in the
	// final phase of the lifecycle sequencer so /healthz and /readyz
	// keep serving (with status="shutting_down") for the entire drain
	// window. Closing the probe listener in PhaseStopAccepting (the old
	// behavior — `probeSrv.Shutdown` called eagerly from run.go below)
	// caused the kubelet to see TCP connect-refused instead of an
	// unready 503 during the drain, which is the wrong K8s preStop
	// signal and broke the e2e graceful-shutdown contract.
	lcMod.RegisterShutdown(lifecycle.ShutdownHook{
		Name:  "probe.listener.shutdown",
		Phase: lifecycle.PhaseCloseConnections,
		Run: func(ctx context.Context) error {
			return probeSrv.Shutdown(ctx)
		},
	})

	mux := buildHTTPMux(lcMod, prod)
	// Wrap the outer mux with the production tracer when observability
	// is wired so probe endpoints (/healthz, /readyz, /startup) generate
	// spans alongside the API surface (story #94 AC #2).
	var rootHandler http.Handler = mux
	if prod != nil && prod.obsModule != nil {
		if tr := prod.obsModule.Tracer(); tr != nil {
			rootHandler = handlers.TracingMiddleware(tr.Tracer())(mux)
		}
	}
	srv := &http.Server{
		Handler:           rootHandler,
		ReadHeaderTimeout: cfg.Server.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.Server.HTTP.ReadTimeout,
		WriteTimeout:      cfg.Server.HTTP.WriteTimeout,
		IdleTimeout:       cfg.Server.HTTP.IdleTimeout,
		MaxHeaderBytes:    cfg.Server.HTTP.MaxHeaderBytes,
	}
	// Story #111: when TLS is enabled, attach a tls.Config with the
	// operator-selected MinVersion before ServeTLS runs. ServeTLS will
	// add the cert pair on top of this base config.
	if !cfg.Server.HTTP.Insecure {
		srv.TLSConfig = &tls.Config{ //nolint:gosec // parseTLSMinVersion always returns >= TLS 1.2; Validate rejects anything lower.
			MinVersion: parseTLSMinVersion(cfg.Server.HTTP.TLS.MinVersion),
		}
	}
	if hooks.onServerConfigured != nil {
		hooks.onServerConfigured(srv)
	}

	// Story #207: publish the *http.Server to the production runtime so
	// the http.listener.stop_accepting hook (registered in
	// registerLifecycle) drives srv.Shutdown under the per-phase
	// budget. The hook reads via getHTTPServer; setting it here happens
	// before MarkStartupComplete and before the sequencer can fire, so
	// no early-shutdown race.
	if prod != nil {
		prod.setHTTPServer(srv)
	}

	// Serve in a goroutine; the main goroutine waits on ctx.Done(). When
	// TLS is configured we use ServeTLS; the cleartext path stays on
	// Serve. There is no fallback — TLS misconfiguration is fatal, never
	// silently downgraded.
	serveErr := make(chan error, 1)
	go func() {
		var err error
		if cfg.Server.HTTP.Insecure {
			err = srv.Serve(listener)
		} else {
			err = srv.ServeTLS(listener,
				cfg.Server.HTTP.TLS.CertFile,
				cfg.Server.HTTP.TLS.KeyFile)
		}
		// http.ErrServerClosed is the clean-shutdown sentinel.
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	logger.Info("http server listening", "addr", addr, "insecure", cfg.Server.HTTP.Insecure)
	// S-1.8: warn when the listener binds the wildcard interface AND TLS
	// is off. Both defaults today (`0.0.0.0:8443`, `insecure=true` for
	// dev/test) trip this; production clusters should set
	// server.http.bind explicitly to the loopback interface or disable
	// `insecure`.
	if isWildcardBind(cfg.Server.HTTP.Bind) && cfg.Server.HTTP.Insecure {
		logger.Warn("wildcard bind in insecure mode; restrict server.http.bind or set server.http.insecure=false in production",
			"bind", cfg.Server.HTTP.Bind)
	}
	if hooks.onListening != nil {
		hooks.onListening(addr)
	}

	// Reload coordinator (stories #151, #152). Constructed only when
	// the binary was launched with a real config-file source: tests
	// that build a Config in code don't have a file to reload from
	// (cfg.Source is nil), and skipping the coordinator there keeps
	// every existing test path untouched.
	var coord *reloadCoordinator
	if cfg.Source != nil {
		coord = newReloadCoordinator(cfg, logger, levelVar)
		if prod != nil && prod.reloadTopicCatalog != nil {
			coord.registerHotApply(func(_, _ *Config) { prod.reloadTopicCatalog() })
		}
		// Drive the SIGHUP seam: the lifecycle dispatcher fans the
		// signal to whatever handler is currently registered. The
		// coordinator owns the full reload — load + validate +
		// immutable-rejection + hot-apply — and does not need ctx
		// (loadConfig blocks on the operator's local disk).
		lcMod.SetReloadHandler(func(_ context.Context) {
			coord.reload(reloadTriggerSIGHUP)
		})
		// Watch ${file:...} secret paths for rotation. Vault Agent /
		// cert-manager rotate without signaling rights into the pod —
		// the watcher closes that gap (story #152).
		coord.startSecretFileWatcher(ctx, cfg.Deployment.SecretFilePollInterval)
	}

	// Audit B-3: only flip startup_complete after every component (just
	// the lifecycle module + listener for now) has come up cleanly. When
	// B-4's storage/handlers/pipeline wiring lands, those modules also
	// register before this call.
	lcMod.MarkStartupComplete()
	reg.markStartupComplete()
	if hooks.onStartupComplete != nil {
		hooks.onStartupComplete()
	}

	// Wait for shutdown signal (caller cancels ctx) or for either the
	// main or probe server to die unexpectedly.
	select {
	case err := <-serveErr:
		// Pre-shutdown serve failure: ask lifecycle to wind down so any
		// registered hooks still get called.
		_ = probeSrv.Close()
		lcMod.RequestShutdown(context.Background(), "http_serve_error")
		_ = lcMod.WaitForExit(context.Background())
		if err != nil {
			return fmt.Errorf("http serve: %w", err)
		}
		return nil
	case err := <-probeServeErr:
		_ = srv.Close()
		lcMod.RequestShutdown(context.Background(), "probe_serve_error")
		_ = lcMod.WaitForExit(context.Background())
		if err != nil {
			return fmt.Errorf("probe serve: %w", err)
		}
		return nil
	case <-ctx.Done():
		// Fall through to graceful shutdown.
	}

	logger.Info("shutdown initiated", "reason", "context_canceled")
	lcMod.RequestShutdown(context.Background(), "context_canceled")
	reg.markShutdownInProgress()
	if hooks.onShutdownStart != nil {
		hooks.onShutdownStart(reg)
	}

	// Bound shutdown by lifecycle.shutdown_grace_period.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Lifecycle.ShutdownGracePeriod)
	defer cancel()

	// OP #338: do NOT close the probe listener here. The kubelet must
	// keep observing /readyz=503 status="shutting_down" throughout the
	// drain — closing the listener early surfaces as connect-refused,
	// not as an unready signal, and trips both K8s preStop semantics
	// and the production graceful-shutdown contract. The probe
	// listener's shutdown is now registered as a
	// PhaseCloseConnections hook (see lcMod.RegisterShutdown above)
	// so it fires after the main listener has drained.
	shutdownErr := srv.Shutdown(shutdownCtx)
	if hooks.shutdownErr != nil {
		shutdownErr = hooks.shutdownErr()
	}
	// OP #338: probe goroutine drain happens AFTER lcMod.WaitForExit
	// below — the probe listener now shuts down via the lifecycle
	// PhaseCloseConnections hook, so its goroutine cannot exit until
	// the sequencer reaches Phase 4. Doing the wait here would
	// always time out the shutdownCtx for no reason.
	if shutdownErr != nil {
		// On grace-period exhaustion srv.Shutdown returns context.DeadlineExceeded.
		// We log and force-close so the goroutine exits. The Close error
		// is also logged — silently dropping it would hide listener/file
		// descriptor leaks during shutdown (S-1.5).
		logger.Warn("graceful shutdown exceeded budget; forcing close", "err", shutdownErr.Error())
		closeFn := srv.Close
		if hooks.forceClose != nil {
			closeFn = hooks.forceClose
		}
		if cErr := closeFn(); cErr != nil {
			logger.Warn("force close failed", "err", cErr.Error())
		}
	}

	// Wait for the serve goroutine to actually exit so we don't return while
	// it's still touching the listener.
	waitCtx, waitCancel := context.WithTimeout(context.Background(), cfg.Lifecycle.ShutdownGracePeriod)
	defer waitCancel()
	select {
	case err := <-serveErr:
		if err != nil {
			_ = lcMod.WaitForExit(waitCtx)
			return fmt.Errorf("http serve after shutdown: %w", err)
		}
	case <-waitCtx.Done():
		_ = lcMod.WaitForExit(context.Background())
		return errors.New("http serve goroutine did not exit within budget")
	}

	// Drain the lifecycle sequencer before returning. PhaseCloseConnections
	// runs the probe.listener.shutdown hook — once that completes, the
	// probe Serve goroutine exits.
	_ = lcMod.WaitForExit(waitCtx)

	// OP #338: drain the probe serve goroutine after PhaseCloseConnections
	// has shut its listener. Bounded so a stuck listener cannot pin the
	// process; in practice the goroutine returns within microseconds of
	// probeSrv.Shutdown completing.
	select {
	case <-probeServeErr:
	case <-time.After(2 * time.Second):
		_ = probeSrv.Close()
		<-probeServeErr
	}

	logger.Info("shutdown complete")
	return nil
}

// buildHTTPMux assembles the production HTTP handler for the main
// (auth-protected) listener. Probes are NOT mounted here; they live on
// the dedicated probe listener (S-118) so a buggy auth wrap cannot 401
// a kubelet probe. The auth-protected FHIR API sits behind
// RegisterRoutes' middleware; the public CapabilityStatement
// (RegisterPublicRoutes' /metadata) sits on the bare chi router so
// FHIR conformance probes reach it without a bearer token (story #93).
// Probe-only mode (no DB) keeps the legacy `/metadata` stub so the
// existing smoke tests continue to function.
func buildHTTPMux(_ *lifecycle.LifecycleModule, prod *productionRuntime) http.Handler {
	mux := http.NewServeMux()

	if prod == nil || prod.router == nil {
		mux.HandleFunc("/metadata", makeMetadata())
		return mux
	}
	// Mount the production router on every path.
	mux.Handle("/", prod.router)
	return mux
}

// buildProbeMux assembles the unauthenticated probe handler. It serves
// /healthz, /readyz, /startup and nothing else — every other path
// returns 404. The kubelet hits this on cfg.Server.HTTP.ProbeBind.
func buildProbeMux(lcMod *lifecycle.LifecycleModule) http.Handler {
	mux := http.NewServeMux()
	probes := lcMod.Probes()
	mux.Handle("/healthz", probes.Healthz)
	mux.Handle("/readyz", probes.Readyz)
	mux.Handle("/startup", probes.Startup)
	return mux
}

// isWildcardBind reports whether bind targets every interface
// (`0.0.0.0:port` or `:port`).
func isWildcardBind(bind string) bool {
	if bind == "" {
		return false
	}
	host, _, err := net.SplitHostPort(bind)
	if err != nil {
		return false
	}
	return host == "" || host == "0.0.0.0" || host == "::"
}

// parseTLSMinVersion maps the operator-facing MinVersion string ("1.2"
// or "1.3") to the matching tls.VersionTLS* constant. Empty string
// (Validate normalizes this to "1.3" first) and unknown values both
// fall back to TLS 1.3 — this function is a last-line defense, the real
// rejection is in Validate.
func parseTLSMinVersion(v string) uint16 {
	switch v {
	case "1.2":
		return tls.VersionTLS12
	case "1.3", "":
		return tls.VersionTLS13
	default:
		return tls.VersionTLS13
	}
}

// logFormatOrDefault preserves the documented JSON default when the
// operator omits deployment.log_format. Story #160 AC: "Empty-format
// MUST default to `json` (preserving today's default)."
func logFormatOrDefault(format string) string {
	if format == "" {
		return "json"
	}
	return format
}

func slogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
