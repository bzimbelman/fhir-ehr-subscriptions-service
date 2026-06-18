// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

import (
	"strings"
	"testing"
	"time"
)

// S-9.2: Validate must reject a config where PersistTimeout exceeds
// ShutdownDrainGrace. Otherwise an in-flight persist (which is
// deliberately decoupled from the request ctx per LLD §5.7) can outlive
// the supervisor's drain window — the supervisor force-closes the conn
// while the persist is still running, which leaves an orphaned write
// against the database. The supervisor's drain budget MUST be at least
// as long as a single persist's worst-case duration.
func TestListenerConfig_Validate_RejectsPersistTimeoutGreaterThanDrainGrace(t *testing.T) {
	cfg := ListenerConfig{
		Endpoints: []EndpointConfig{
			{Name: "adt", Bind: "127.0.0.1:0"},
		},
		MaxMessageBytes:    1 << 20,
		PersistTimeout:     10 * time.Second,
		ShutdownDrainGrace: 5 * time.Second, // less than PersistTimeout
		NackThenDropAfter:  3,
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatalf("Validate accepted PersistTimeout(10s) > ShutdownDrainGrace(5s); want error")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "persist_timeout") {
		t.Fatalf("error %q should reference persist_timeout", err)
	}
	if !strings.Contains(msg, "shutdown_drain_grace") && !strings.Contains(msg, "drain") {
		t.Fatalf("error %q should reference shutdown_drain_grace / drain", err)
	}
}

// S-9.2: Validate accepts the equality case (PersistTimeout ==
// ShutdownDrainGrace). Operators tuning to the boundary should not be
// rejected.
func TestListenerConfig_Validate_AcceptsPersistTimeoutEqualToDrainGrace(t *testing.T) {
	cfg := ListenerConfig{
		Endpoints: []EndpointConfig{
			{Name: "adt", Bind: "127.0.0.1:0"},
		},
		MaxMessageBytes:    1 << 20,
		PersistTimeout:     5 * time.Second,
		ShutdownDrainGrace: 5 * time.Second,
		NackThenDropAfter:  3,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate rejected equal PersistTimeout/ShutdownDrainGrace: %v", err)
	}
}

// S-9.2: Validate accepts the normal case (PersistTimeout <
// ShutdownDrainGrace).
func TestListenerConfig_Validate_AcceptsPersistTimeoutLessThanDrainGrace(t *testing.T) {
	cfg := ListenerConfig{
		Endpoints: []EndpointConfig{
			{Name: "adt", Bind: "127.0.0.1:0"},
		},
		MaxMessageBytes:    1 << 20,
		PersistTimeout:     2 * time.Second,
		ShutdownDrainGrace: 10 * time.Second,
		NackThenDropAfter:  3,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate rejected PersistTimeout(2s) < ShutdownDrainGrace(10s): %v", err)
	}
}
