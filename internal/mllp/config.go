// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

import (
	"errors"
	"fmt"
	"time"
)

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
	// Must be strictly less than the host's storage.statement_timeout.
	PersistTimeout time.Duration

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
	if c.NackThenDropAfter <= 0 {
		return errors.New("mllp listener: nack_then_drop_after must be > 0")
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
