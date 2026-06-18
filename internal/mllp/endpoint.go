// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"
)

// endpoint owns one TCP accept loop and the set of in-flight connections
// for one EndpointConfig. It is created and managed by Listener; nothing
// outside the package constructs one directly.
type endpoint struct {
	cfg       EndpointConfig
	listenCfg ListenerConfig
	persister Persister
	metrics   MetricsEmitter
	logger    Logger
	parent    *Listener // for admission control (B-19)

	listener net.Listener

	connsMu sync.Mutex
	conns   map[net.Conn]struct{}
	connsWG sync.WaitGroup
}

func newEndpoint(cfg EndpointConfig, lcfg ListenerConfig, p Persister, m MetricsEmitter, log Logger) *endpoint {
	return &endpoint{
		cfg:       cfg,
		listenCfg: lcfg,
		persister: p,
		metrics:   m,
		logger:    log,
		conns:     map[net.Conn]struct{}{},
	}
}

// bind opens the TCP listening socket. When a TLS config is set on the
// listener, the socket is wrapped with tls.NewListener so every accepted
// connection negotiates TLS before any HL7 bytes flow (B-20).
func (e *endpoint) bind() error {
	l, err := net.Listen("tcp", e.cfg.Bind)
	if err != nil {
		return err
	}
	if e.listenCfg.TLS != nil {
		l = tls.NewListener(l, e.tlsServerConfig())
	}
	e.listener = l
	e.metrics.Set(MetricActiveConnections, 0, map[string]string{"listener_endpoint": e.cfg.Name})
	return nil
}

// tlsServerConfig returns a *tls.Config derived from listenCfg.TLS with
// the security defaults the listener insists on (TLS 1.2 floor, mTLS
// when enabled).
func (e *endpoint) tlsServerConfig() *tls.Config {
	src := e.listenCfg.TLS
	cfg := src.Config.Clone()
	if cfg.MinVersion == 0 {
		cfg.MinVersion = tls.VersionTLS12
	}
	if src.RequireAndVerifyClientCert {
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
		cfg.ClientCAs = src.ClientCAs
	}
	return cfg
}

// addr returns the bound address. Useful for tests using ":0".
func (e *endpoint) addr() net.Addr {
	if e.listener == nil {
		return nil
	}
	return e.listener.Addr()
}

// run drives the accept loop. It returns when ctx is canceled or the
// listening socket is closed.
//
// Each accepted TCP socket is gated by Listener.admitConnection (B-19);
// connections beyond MaxConnections / MaxConnectionsPerIP are closed
// immediately and a WARN log records the offending peer. The semaphore
// release closure is bound to the connection lifetime so a slot is
// reclaimed on disconnect even if the handler panics.
func (e *endpoint) run(ctx context.Context) {
	endpointLabels := map[string]string{"listener_endpoint": e.cfg.Name}
	for {
		conn, err := e.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			e.metrics.Inc(MetricAcceptErrorsTotal, endpointLabels)
			e.logger.Warn("mllp_accept_error", map[string]any{
				"listener_endpoint": e.cfg.Name,
				"error":             err.Error(),
			})
			// brief backoff to avoid an accept tight loop
			select {
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}

		remoteAddr := conn.RemoteAddr().String()
		decision := admissionDecision{allow: true}
		if e.parent != nil {
			decision = e.parent.admitConnection(remoteAddr)
		}
		if !decision.allow {
			e.metrics.Inc(MetricConnectionsRefusedTotal, endpointLabels)
			e.logger.Warn("mllp_connection_refused", map[string]any{
				"listener_endpoint":      e.cfg.Name,
				"peer_addr":              remoteAddr,
				"reason":                 decision.reason,
				"max_connections":        e.listenCfg.MaxConnections,
				"max_connections_per_ip": e.listenCfg.MaxConnectionsPerIP,
			})
			_ = conn.Close()
			continue
		}

		e.trackConn(conn)
		e.connsWG.Add(1)
		go func(c net.Conn, release func()) {
			defer e.connsWG.Done()
			defer e.untrackConn(c)
			defer release()
			HandleConnectionWithLogger(ctx, c, e.cfg, e.listenCfg, e.persister, e.metrics, e.logger, remoteAddr)
		}(conn, decision.release)
	}
}

func (e *endpoint) trackConn(c net.Conn) {
	e.connsMu.Lock()
	e.conns[c] = struct{}{}
	count := len(e.conns)
	e.connsMu.Unlock()
	e.metrics.Set(MetricActiveConnections, float64(count),
		map[string]string{"listener_endpoint": e.cfg.Name})
}

func (e *endpoint) untrackConn(c net.Conn) {
	e.connsMu.Lock()
	delete(e.conns, c)
	count := len(e.conns)
	e.connsMu.Unlock()
	e.metrics.Set(MetricActiveConnections, float64(count),
		map[string]string{"listener_endpoint": e.cfg.Name})
}

// stopAccepting closes the listening socket. Existing connections continue.
func (e *endpoint) stopAccepting() {
	if e.listener != nil {
		_ = e.listener.Close()
	}
}

// closeAllConns force-closes every tracked connection. Used on hard
// shutdown after the drain deadline elapses.
func (e *endpoint) closeAllConns() {
	e.connsMu.Lock()
	conns := make([]net.Conn, 0, len(e.conns))
	for c := range e.conns {
		conns = append(conns, c)
	}
	e.connsMu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// activeConnCount returns the number of in-flight connections.
func (e *endpoint) activeConnCount() int {
	e.connsMu.Lock()
	defer e.connsMu.Unlock()
	return len(e.conns)
}

// waitForDrain blocks until all per-connection goroutines exit.
func (e *endpoint) waitForDrain() {
	e.connsWG.Wait()
}
