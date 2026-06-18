// Copyright the fhir-subscriptions-foss authors.
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
	"time"
)

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
}

// run is the production entry point used by main(). It is a thin wrapper
// around runWithHooks with no test hooks installed.
func run(ctx context.Context, cfg *Config, logOut io.Writer) error {
	return runWithHooks(ctx, cfg, logOut, runHooks{})
}

// runWithHooks owns the full process lifetime for one boot:
//   - validates the config
//   - constructs the lifecycle registry
//   - binds and serves the probe HTTP server
//   - flips startup_complete once the listener is up
//   - waits on ctx.Done() (the signal handler cancels ctx)
//   - drives graceful shutdown bounded by lifecycle.shutdown_grace_period
//
// The function returns nil on a clean graceful shutdown and a non-nil error
// for any startup or shutdown failure.
func runWithHooks(ctx context.Context, cfg *Config, logOut io.Writer, hooks runHooks) error {
	if err := cfg.Validate(); err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(logOut, &slog.HandlerOptions{Level: slogLevel(cfg.Deployment.LogLevel)}))
	logger.Info(banner(cfg.Deployment.FacilityID, cfg.Adapter.ID),
		"facility_id", cfg.Deployment.FacilityID,
		"adapter_id", cfg.Adapter.ID,
		"version", Version,
		"commit", Commit,
		"environment", cfg.Deployment.Environment,
	)

	reg := newLifecycleRegistry()

	// Pre-bind the listener so we know the chosen port before starting the
	// server goroutine. Lets tests use bind=":0" without a race.
	listener, err := net.Listen("tcp", cfg.Server.HTTP.Bind)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Server.HTTP.Bind, err)
	}
	addr := listener.Addr().String()

	srv := &http.Server{
		Handler:           probeMux(reg),
		ReadHeaderTimeout: 5 * time.Second,
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

	// The HTTP server is bound and accepting. Mark startup complete and notify
	// any test hook.
	reg.markStartupComplete()
	logger.Info("http server listening", "addr", addr, "insecure", cfg.Server.HTTP.Insecure)
	if hooks.onListening != nil {
		hooks.onListening(addr)
	}

	// Wait for shutdown signal (caller cancels ctx) or for the server to die
	// unexpectedly.
	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("http serve: %w", err)
		}
		// Server returned without error before shutdown was triggered.
		return nil
	case <-ctx.Done():
		// Fall through to graceful shutdown.
	}

	logger.Info("shutdown initiated", "reason", "context_canceled")
	reg.markShutdownInProgress()
	if hooks.onShutdownStart != nil {
		hooks.onShutdownStart(reg)
	}

	// Bound shutdown by lifecycle.shutdown_grace_period.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Lifecycle.ShutdownGracePeriod)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		// On grace-period exhaustion srv.Shutdown returns context.DeadlineExceeded.
		// We log and force-close so the goroutine exits.
		logger.Warn("graceful shutdown exceeded budget; forcing close", "err", err.Error())
		_ = srv.Close()
	}

	// Wait for the serve goroutine to actually exit so we don't return while
	// it's still touching the listener.
	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("http serve after shutdown: %w", err)
		}
	case <-time.After(cfg.Lifecycle.ShutdownGracePeriod + 2*time.Second):
		return errors.New("http serve goroutine did not exit within budget")
	}

	logger.Info("shutdown complete")
	return nil
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
