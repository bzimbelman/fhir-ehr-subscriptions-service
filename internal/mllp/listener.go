// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// Listener is the supervisor described in LLD §3. One Listener owns a set
// of bound endpoints, a fixed Persister, a fixed MetricsEmitter, and the
// graceful-shutdown signal.
type Listener struct {
	cfg       ListenerConfig
	persister Persister
	metrics   MetricsEmitter
	logger    Logger

	endpoints []*endpoint

	cancel  context.CancelFunc
	runCtx  context.Context
	startMu sync.Mutex
	started bool
	stopMu  sync.Mutex
	stopped bool
}

// New constructs a Listener but does not bind sockets. Call Start to bind
// and begin accepting. If persister is nil, Start returns an error;
// metrics defaults to a no-op emitter.
func New(cfg ListenerConfig, p Persister, m MetricsEmitter, log Logger) *Listener {
	if m == nil {
		m = nopMetrics{}
	}
	if log == nil {
		log = nopLogger{}
	}
	return &Listener{
		cfg:       cfg.withDefaults(),
		persister: p,
		metrics:   m,
		logger:    log,
	}
}

// Start binds every configured endpoint and spawns its accept loop.
// Returns an error on the first bind failure (per LLD §10: bind failure
// fails fast). On successful return, every endpoint is bound and ready.
//
// The returned Listener owns the lifecycle until Shutdown is called.
func (l *Listener) Start(ctx context.Context) error {
	l.startMu.Lock()
	defer l.startMu.Unlock()
	if l.started {
		return errors.New("mllp listener: already started")
	}
	if l.persister == nil {
		return errors.New("mllp listener: persister is required")
	}
	if err := l.cfg.Validate(); err != nil {
		return err
	}

	l.runCtx, l.cancel = context.WithCancel(context.Background())

	endpoints := make([]*endpoint, 0, len(l.cfg.Endpoints))
	for _, epCfg := range l.cfg.Endpoints {
		ep := newEndpoint(epCfg, l.cfg, l.persister, l.metrics, l.logger)
		if err := ep.bind(); err != nil {
			// Roll back: close any endpoint we already bound.
			for _, prev := range endpoints {
				prev.stopAccepting()
			}
			l.cancel()
			return fmt.Errorf("bind %s (%s): %w", epCfg.Name, epCfg.Bind, err)
		}
		endpoints = append(endpoints, ep)
	}
	l.endpoints = endpoints
	for _, ep := range l.endpoints {
		go ep.run(l.runCtx)
		l.logger.Info("mllp_listener_bound", map[string]any{
			"listener_endpoint": ep.cfg.Name,
			"bind":              ep.cfg.Bind,
			"addr":              ep.addr().String(),
		})
	}
	l.started = true
	return nil
}

// Addr returns the bound address of the named endpoint, or nil if no such
// endpoint exists. Tests use this to discover the port assigned by ":0".
func (l *Listener) Addr(endpointName string) net.Addr {
	for _, ep := range l.endpoints {
		if ep.cfg.Name == endpointName {
			return ep.addr()
		}
	}
	return nil
}

// Status returns a snapshot of per-endpoint readiness for /readyz.
func (l *Listener) Status() Status {
	out := Status{Endpoints: make([]EndpointStatus, 0, len(l.endpoints))}
	for _, ep := range l.endpoints {
		out.Endpoints = append(out.Endpoints, EndpointStatus{
			Name:              ep.cfg.Name,
			Bound:             ep.listener != nil,
			ActiveConnections: ep.activeConnCount(),
		})
	}
	return out
}

// Shutdown begins graceful shutdown: stops accepting new connections,
// waits for in-flight connections to drain (up to ShutdownDrainGrace),
// then force-closes anything still open. It blocks until done.
func (l *Listener) Shutdown(ctx context.Context) error {
	l.stopMu.Lock()
	if l.stopped {
		l.stopMu.Unlock()
		return nil
	}
	l.stopped = true
	l.stopMu.Unlock()

	if !l.started {
		return nil
	}

	// Phase 1: stop accepting on every endpoint, signal connections to drain.
	for _, ep := range l.endpoints {
		ep.stopAccepting()
	}
	if l.cancel != nil {
		l.cancel()
	}

	// Phase 2: wait until every connection goroutine has exited or the
	// supplied ctx (or the configured drain grace) elapses.
	deadline := time.Now().Add(l.cfg.ShutdownDrainGrace)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	drained := make(chan struct{})
	go func() {
		for _, ep := range l.endpoints {
			ep.waitForDrain()
		}
		close(drained)
	}()

	select {
	case <-drained:
		l.logger.Info("mllp_listener_shutdown_complete", nil)
		return nil
	case <-time.After(time.Until(deadline)):
		// Hard close anything remaining.
		for _, ep := range l.endpoints {
			ep.closeAllConns()
		}
		// Wait for the goroutines to actually exit after the force close.
		<-drained
		l.logger.Warn("mllp_listener_shutdown_forced", nil)
		return nil
	}
}

// Status is the readiness snapshot returned by Listener.Status.
type Status struct {
	Endpoints []EndpointStatus
}

// EndpointStatus is one endpoint's per-readiness view.
type EndpointStatus struct {
	Name              string
	Bound             bool
	ActiveConnections int
}
