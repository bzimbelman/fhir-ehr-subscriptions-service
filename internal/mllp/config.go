// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"time"
)

// TLSConfig configures TLS / mTLS for an MLLP listener (B-20). When
// non-nil, the listener wraps every accepted TCP socket with
// tls.NewListener, refusing plaintext connections. Operators on a
// hospital network must configure TLS — HL7 messages carry PHI.
type TLSConfig struct {
	// Config is the base *tls.Config. Required when TLSConfig is set.
	// MinVersion defaults to TLS 1.2 when zero. Cipher suites default
	// to the Go standard AEAD suites; operators may override.
	Config *tls.Config

	// RequireAndVerifyClientCert enables mutual TLS — the listener
	// rejects connections that do not present a client certificate
	// signed by a CA in ClientCAs.
	RequireAndVerifyClientCert bool

	// ClientCAs is the trust pool used to verify peer certificates when
	// RequireAndVerifyClientCert is true. Required for mTLS.
	ClientCAs *x509.CertPool
}

// OnPersistFail enumerates how the listener responds when a persist call
// fails. Per LLD §8: NACK lets the EHR hold and re-send; Drop closes the
// connection and waits for the EHR to reconnect.
type OnPersistFail string

// OnPersistFail values.
const (
	OnPersistFailNack OnPersistFail = "nack"
	OnPersistFailDrop OnPersistFail = "drop"
)

// EndpointConfig is one entry under mllp_listener.endpoints[] in the
// project config (architecture.md §"MLLP Listener — host-provided"). The
// listener binds one TCP socket per EndpointConfig.
type EndpointConfig struct {
	// Name is the configured endpoint name (e.g., "adt-feed"). Recorded on
	// every persisted row and used as a label on every metric.
	Name string

	// Bind is the host:port to listen on (e.g., "0.0.0.0:2575"). Empty
	// disables binding (used in unit tests that drive HandleConnection
	// directly via net.Pipe).
	Bind string

	// AllowedMessageTypes optionally filters by MSH-9 root. Nil/empty list
	// means accept all types. Per LLD §10 open question: when the filter
	// is configured AND MSH-9 cannot be extracted, the message is NACKed
	// with reason msh9_unparseable.
	AllowedMessageTypes []string
}

// ListenerConfig is the validated startup config for the listener as a whole.
type ListenerConfig struct {
	Endpoints []EndpointConfig

	// MaxMessageBytes caps the inter-marker body length. Default 1 MiB.
	MaxMessageBytes int

	// ReadIdleTimeout closes a connection that has been silent this long.
	// Default 60s. A value of 0 disables idle close (useful in tests).
	ReadIdleTimeout time.Duration

	// PersistTimeout bounds the per-message Persist call. Default 5s.
	// Must be strictly less than the host's storage.statement_timeout, and
	// MUST be ≤ ShutdownDrainGrace — the supervisor's drain budget has to
	// cover at least one full persist or the in-flight Persist call (which
	// LLD §5.7 deliberately decouples from request ctx so it can finish
	// after shutdown begins) will outlive the drain window. Validate
	// enforces the cap (S-9.2).
	PersistTimeout time.Duration

	// FrameAssemblyTimeout bounds how long a single inter-marker frame may
	// take to assemble end-to-end. The deadline starts when the framer
	// transitions to in-flight (first 0x0B for a given frame) and resets
	// on every successfully delivered FrameEvent. Default 30s. Implements
	// S-9.1 — without this knob a peer that streams a partial frame can
	// hold a connection slot until the framer's pending-byte cap (S-9.4)
	// trips, which is up to 2× MaxMessageBytes of bandwidth.
	FrameAssemblyTimeout time.Duration

	// AckWriteTimeout bounds a single ACK/NACK frame write back to the
	// peer. Default 2s. The previous code hardcoded the 2s deadline
	// inline; deployments behind a high-latency LB occasionally need
	// to extend it (N-1).
	AckWriteTimeout time.Duration

	// ReadBufBytes is the size of the per-connection scratch buffer
	// that the read loop reuses for each conn.Read call. Default 8 KiB.
	// Larger values reduce syscall pressure on bursty senders at the
	// cost of more idle memory per connection. (N-1.)
	ReadBufBytes int

	// NackThenDropAfter is the consecutive-persist-failure threshold
	// at which the listener drops the connection. Default 5.
	NackThenDropAfter int

	// ShutdownDrainGrace bounds the drain phase. The supervisor uses this
	// when it composes per-listener shutdown into the host lifecycle's
	// shutdown_grace_period.
	ShutdownDrainGrace time.Duration

	// InflightCapPerConn caps per-connection unfinished persist calls.
	// Default 64. Set to 0 to disable the per-connection backpressure cap.
	InflightCapPerConn int

	// OnPersistFail selects the behavior on persist failure. Default Nack.
	OnPersistFail OnPersistFail

	// MaxConnections caps the total number of concurrent connections
	// across all endpoints (B-19). When the cap is reached, additional
	// accepted TCP sockets are immediately closed and a WARN log line
	// records the offending peer. Zero (default) disables the cap.
	// Operators should size this from the upstream EHR's expected
	// concurrent senders plus headroom.
	MaxConnections int

	// MaxConnectionsPerIP caps concurrent connections from a single
	// remote IP (B-19). A misbehaving sender that opens hundreds of
	// connections can no longer monopolize accept-loop capacity. Zero
	// (default) disables the per-IP cap.
	MaxConnectionsPerIP int

	// TLS, when non-nil, wraps every accepted TCP socket in TLS (B-20).
	// HL7 messages carry PHI; running plaintext on a hospital network
	// is an OCR / HIPAA finding. Required for production deployments.
	TLS *TLSConfig
}

// Validate returns an error if any required field is missing or any tunable
// is out of range. Mirrors the LLD §6 "validation runs at startup, before
// any socket binds" rule.
func (c ListenerConfig) Validate() error {
	if len(c.Endpoints) == 0 {
		return errors.New("mllp listener: at least one endpoint required")
	}
	seen := map[string]bool{}
	for i, ep := range c.Endpoints {
		if ep.Name == "" {
			return fmt.Errorf("mllp listener: endpoints[%d].name is required", i)
		}
		if seen[ep.Name] {
			return fmt.Errorf("mllp listener: duplicate endpoint name %q", ep.Name)
		}
		seen[ep.Name] = true
		if ep.Bind == "" {
			return fmt.Errorf("mllp listener: endpoints[%d].bind is required", i)
		}
	}
	if c.MaxMessageBytes <= 0 {
		return errors.New("mllp listener: max_message_bytes must be > 0")
	}
	if c.PersistTimeout <= 0 {
		return errors.New("mllp listener: persist_timeout must be > 0")
	}
	// S-9.2: a misconfigured operator with PersistTimeout >
	// ShutdownDrainGrace causes the supervisor to force-close in-flight
	// connections while their persists are still running, leaking work.
	// We compare only when ShutdownDrainGrace is set; the zero/negative
	// case is normalized in withDefaults but Validate runs first.
	if c.ShutdownDrainGrace > 0 && c.PersistTimeout > c.ShutdownDrainGrace {
		return fmt.Errorf("mllp listener: persist_timeout (%s) must be <= shutdown_drain_grace (%s)",
			c.PersistTimeout, c.ShutdownDrainGrace)
	}
	if c.FrameAssemblyTimeout < 0 {
		return errors.New("mllp listener: frame_assembly_timeout must be >= 0")
	}
	if c.NackThenDropAfter <= 0 {
		return errors.New("mllp listener: nack_then_drop_after must be > 0")
	}
	if c.MaxConnections < 0 {
		return errors.New("mllp listener: max_connections must be >= 0")
	}
	if c.MaxConnectionsPerIP < 0 {
		return errors.New("mllp listener: max_connections_per_ip must be >= 0")
	}
	if c.TLS != nil {
		if c.TLS.Config == nil {
			return errors.New("mllp listener: tls.config required when TLS is set")
		}
		if c.TLS.RequireAndVerifyClientCert && c.TLS.ClientCAs == nil {
			return errors.New("mllp listener: tls.client_cas required when require_and_verify_client_cert is true")
		}
	}
	return nil
}

// withDefaults returns a copy of c with zero-valued tunables filled in.
// Called by Start so callers may pass a partially-populated config.
func (c ListenerConfig) withDefaults() ListenerConfig {
	if c.MaxMessageBytes <= 0 {
		c.MaxMessageBytes = 1 << 20 // 1 MiB
	}
	if c.ReadIdleTimeout < 0 {
		c.ReadIdleTimeout = 0
	}
	if c.ReadIdleTimeout == 0 {
		c.ReadIdleTimeout = 60 * time.Second
	}
	if c.PersistTimeout <= 0 {
		c.PersistTimeout = 5 * time.Second
	}
	if c.FrameAssemblyTimeout < 0 {
		c.FrameAssemblyTimeout = 0
	}
	if c.FrameAssemblyTimeout == 0 {
		c.FrameAssemblyTimeout = 30 * time.Second
	}
	if c.AckWriteTimeout <= 0 {
		c.AckWriteTimeout = 2 * time.Second
	}
	if c.ReadBufBytes <= 0 {
		c.ReadBufBytes = 8192
	}
	if c.NackThenDropAfter <= 0 {
		c.NackThenDropAfter = 5
	}
	if c.ShutdownDrainGrace <= 0 {
		c.ShutdownDrainGrace = 10 * time.Second
	}
	if c.InflightCapPerConn < 0 {
		c.InflightCapPerConn = 0
	}
	if c.OnPersistFail == "" {
		c.OnPersistFail = OnPersistFailNack
	}
	return c
}
