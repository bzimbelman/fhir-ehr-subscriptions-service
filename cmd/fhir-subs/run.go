// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/lifecycle"
)

// testForceCloseHook is a test seam: when non-nil, the shutdown path
// calls it instead of srv.Close after the grace period is exhausted, so
// tests can assert that a Close failure is logged (S-1.5). Production
// builds leave this nil.
var testForceCloseHook func() error

// testShutdownErrHook is a test seam: when non-nil, runWithHooks treats
// it as the result of srv.Shutdown so the grace-exhaustion / Close
// branch can be exercised deterministically without timing tricks
// (S-1.5).
var testShutdownErrHook func() error

// runHooks lets tests observe internal state transitions deterministically. In
// production the hooks are nil. The hooks are intentionally minimal: enough to
// drive the shutdown-window readiness assertion without freezing test time.
type runHooks struct {
	// onListening fires after the HTTP server is bound and accepting traffic.
	// addr is the actual bound address (matters when bind uses :0).
	onListening func(addr string)
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

	logger := slog.New(slog.NewJSONHandler(logOut, &slog.HandlerOptions{Level: slogLevel(cfg.Deployment.LogLevel)}))
	logger.Info(banner(cfg.Deployment.FacilityID, cfg.Adapter.ID),
		"facility_id", cfg.Deployment.FacilityID,
		"adapter_id", cfg.Adapter.ID,
		"version", Version,
		"commit", Commit,
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

	// Pre-bind the listener so we know the chosen port before starting the
	// server goroutine. Lets tests use bind=":0" without a race.
	listener, err := net.Listen("tcp", cfg.Server.HTTP.Bind)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Server.HTTP.Bind, err)
	}
	addr := listener.Addr().String()

	mux := buildHTTPMux(lcMod)
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: cfg.Server.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.Server.HTTP.ReadTimeout,
		WriteTimeout:      cfg.Server.HTTP.WriteTimeout,
		IdleTimeout:       cfg.Server.HTTP.IdleTimeout,
		MaxHeaderBytes:    cfg.Server.HTTP.MaxHeaderBytes,
	}
	if hooks.onServerConfigured != nil {
		hooks.onServerConfigured(srv)
	}

	// Serve in a goroutine; the main goroutine waits on ctx.Done().
	serveErr := make(chan error, 1)
	go func() {
		err := srv.Serve(listener)
		// http.ErrServerClosed is the clean-shutdown sentinel.
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	logger.Info("http server listening", "addr", addr, "insecure", cfg.Server.HTTP.Insecure)
	if hooks.onListening != nil {
		hooks.onListening(addr)
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

	// Wait for shutdown signal (caller cancels ctx) or for the server to die
	// unexpectedly. The lifecycle module also installs SIGTERM/SIGINT
	// handlers, so RequestShutdown can fire from either path.
	select {
	case err := <-serveErr:
		// Pre-shutdown serve failure: ask lifecycle to wind down so any
		// registered hooks still get called.
		lcMod.RequestShutdown(context.Background(), "http_serve_error")
		_ = lcMod.WaitForExit(context.Background())
		if err != nil {
			return fmt.Errorf("http serve: %w", err)
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

	shutdownErr := srv.Shutdown(shutdownCtx)
	if testShutdownErrHook != nil {
		shutdownErr = testShutdownErrHook()
	}
	if shutdownErr != nil {
		// On grace-period exhaustion srv.Shutdown returns context.DeadlineExceeded.
		// We log and force-close so the goroutine exits. The Close error
		// is also logged — silently dropping it would hide listener/file
		// descriptor leaks during shutdown (S-1.5).
		logger.Warn("graceful shutdown exceeded budget; forcing close", "err", shutdownErr.Error())
		closeFn := srv.Close
		if testForceCloseHook != nil {
			closeFn = testForceCloseHook
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

	// Drain the lifecycle sequencer before returning.
	_ = lcMod.WaitForExit(waitCtx)

	logger.Info("shutdown complete")
	return nil
}

// buildHTTPMux assembles the production HTTP handler. Today it serves the
// lifecycle probe surface and the legacy /metadata stub. When B-4's full
// API + pipeline wiring lands, this is where handlers.RegisterRoutes is
// mounted behind auth + observability middleware.
func buildHTTPMux(lcMod *lifecycle.LifecycleModule) http.Handler {
	mux := http.NewServeMux()
	probes := lcMod.Probes()
	mux.Handle("/healthz", probes.Healthz)
	mux.Handle("/readyz", probes.Readyz)
	mux.Handle("/startup", probes.Startup)
	mux.HandleFunc("/metadata", makeMetadata())
	return mux
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
