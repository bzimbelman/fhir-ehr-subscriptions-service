// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/tls"
	"net/http"
	"testing"
)

// TestRestHookActivator_DefaultTLSMinVersionIs13 covers OP #203 for
// the rest-hook activator path. The activator was the last hot path
// pinned to TLS 1.2; rest-hook delivery, email STARTTLS, MLLP TLS,
// and the HTTP server listener already default to 1.3. Without
// unification, a deployment that lands TLS 1.3 on every other
// subscriber path silently downgrades the handshake POST to TLS 1.2.
func TestRestHookActivator_DefaultTLSMinVersionIs13(t *testing.T) {
	t.Parallel()

	act := newRestHookActivator(restHookActivatorOptions{
		// Leave TLSMinVersion zero so the default fires.
	})
	if act == nil || act.client == nil {
		t.Fatal("newRestHookActivator returned nil client")
	}
	tr, ok := act.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("activator transport is %T; want *http.Transport", act.client.Transport)
	}
	if tr.TLSClientConfig == nil {
		t.Fatal("activator TLSClientConfig is nil")
	}
	if got := tr.TLSClientConfig.MinVersion; got != tls.VersionTLS13 {
		t.Errorf("activator TLS MinVersion = 0x%04x; want 0x%04x (TLS 1.3) per OP #203 unification", got, uint16(tls.VersionTLS13))
	}
}

// TestRestHookActivator_OperatorOverrideHonored confirms operators
// with legacy subscribers can still pin TLS 1.2 explicitly. The
// override is the only path to a non-1.3 floor — silent downgrades
// are gone.
func TestRestHookActivator_OperatorOverrideHonored(t *testing.T) {
	t.Parallel()

	act := newRestHookActivator(restHookActivatorOptions{
		TLSMinVersion: tls.VersionTLS12,
	})
	tr := act.client.Transport.(*http.Transport)
	if got := tr.TLSClientConfig.MinVersion; got != tls.VersionTLS12 {
		t.Errorf("operator-pinned MinVersion = 0x%04x; want 0x%04x (operator opt-in)", got, uint16(tls.VersionTLS12))
	}
}

// TestTLSUniformity_EveryChannelDefaultsTo13 pins the cross-channel
// invariant: rest-hook delivery, the rest-hook activator, the email
// channel's synthesized STARTTLS config, and the HTTP server's
// MinVersion (parsed from the operator-facing string) all default
// to TLS 1.3. The Three TLS Floors For Three Same-Subscriber Paths
// regression tracked by OP #203 is gone the moment any of these
// drifts — this test is the canary.
func TestTLSUniformity_EveryChannelDefaultsTo13(t *testing.T) {
	t.Parallel()

	// Activator
	act := newRestHookActivator(restHookActivatorOptions{})
	if got := act.client.Transport.(*http.Transport).TLSClientConfig.MinVersion; got != tls.VersionTLS13 {
		t.Errorf("rest-hook activator default = 0x%04x; want TLS 1.3", got)
	}

	// HTTP server's parser
	if got := parseTLSMinVersion(""); got != tls.VersionTLS13 {
		t.Errorf("HTTP server parseTLSMinVersion(\"\") = 0x%04x; want TLS 1.3", got)
	}
	if got := parseTLSMinVersion("1.3"); got != tls.VersionTLS13 {
		t.Errorf("HTTP server parseTLSMinVersion(\"1.3\") = 0x%04x; want TLS 1.3", got)
	}
}
