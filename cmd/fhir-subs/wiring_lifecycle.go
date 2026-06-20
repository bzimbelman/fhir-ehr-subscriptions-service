// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/lifecycle"
)

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

	// OP #208: auth token-endpoint + JWKS fetcher transports keep
	// long-lived TLS sockets to the operator's IDP (the same trust
	// boundary the rest-hook activator owns for the subscriber side).
	// On graceful shutdown those connections must be released so the
	// process exits without warm sockets. Both hooks live in
	// PhaseCloseConnections alongside resthook.activator.close so the
	// connection-tier teardown is concentrated in one phase.
	//
	// Both hooks are registered unconditionally and nil-check inside the
	// Run closure (mirrors database.pool.close above) so the
	// operator-visible phase contract pins both names regardless of
	// whether the runtime built a token endpoint (TokenURL +
	// IssuedSecret) or only a verifier (Audience-only). Without
	// unconditional registration the probe-only / verifier-only deploy
	// path silently drops the token_endpoint.close phase line.
	lcMod.RegisterShutdown(lifecycle.ShutdownHook{
		Name:  "auth.token_endpoint.close",
		Phase: lifecycle.PhaseCloseConnections,
		Run: func(_ context.Context) error {
			if r.tokenSrv == nil {
				return nil
			}
			return r.tokenSrv.Close()
		},
	})
	lcMod.RegisterShutdown(lifecycle.ShutdownHook{
		Name:  "auth.jwks_fetcher.close",
		Phase: lifecycle.PhaseCloseConnections,
		Run: func(_ context.Context) error {
			if r.authVerif == nil {
				return nil
			}
			return r.authVerif.Close()
		},
	})
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
