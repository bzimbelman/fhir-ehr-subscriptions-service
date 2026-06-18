// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// maybeStartProbeListener binds a dedicated probe HTTP listener when
// cfg.ProbeBind is non-empty. LLD §8 — the listener has tight read/write
// timeouts (200ms each) so a slow probe client cannot tie up the server.
//
// When cfg.ProbeBind is empty, the function is a no-op; the host owns the
// mount point and consumes ProbeHandlers.
//
// Failure to bind is returned as a startup error (LLD §10). Once Start
// returns, the listener stops on the next RegisterShutdown(close-listener)
// or on direct shutdown of the embedded server.
func maybeStartProbeListener(m *LifecycleModule) error {
	if m.cfg.ProbeBind == "" {
		return nil
	}
	mux := http.NewServeMux()
	mux.Handle("/healthz", m.probes.Healthz)
	mux.Handle("/readyz", m.probes.Readyz)
	mux.Handle("/startup", m.probes.Startup)

	ln, err := net.Listen("tcp", m.cfg.ProbeBind)
	if err != nil {
		return errors.Join(errors.New("lifecycle: probe listener bind failed"), err)
	}

	srv := &http.Server{
		Handler:           mux,
		ReadTimeout:       200 * time.Millisecond,
		ReadHeaderTimeout: 200 * time.Millisecond,
		WriteTimeout:      200 * time.Millisecond,
		IdleTimeout:       2 * time.Second,
		MaxHeaderBytes:    4 << 10, // 4 KiB cap per LLD §8
	}
	m.server = srv

	// Register a shutdown hook in the CloseConnections phase so the
	// sequencer takes the listener down with everything else. The hook
	// uses srv.Shutdown so in-flight probe responses finish.
	hook := ShutdownHook{
		Name:  "lifecycle.probe_server",
		Phase: PhaseCloseConnections,
		Run: func(ctx context.Context) error {
			return srv.Shutdown(ctx)
		},
	}
	if err := m.reg.registerShutdown(hook); err != nil {
		return err
	}

	go func() {
		err := srv.Serve(ln)
		if err != nil && err != http.ErrServerClosed {
			m.lctx.Logger.Error("lifecycle probe listener exited", "error", err.Error())
		}
	}()
	return nil
}
