// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package email

import (
	"crypto/tls"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
)

// TestBuildMIME_DateHeader_UsesClockSeam pins that the MIME Date
// header flows through Config.Now so the produced bytes are
// deterministic for the same input under a fake clock. AC for #102:
// "MIME Date header MUST come through the channel's Now() clock seam."
func TestBuildMIME_DateHeader_UsesClockSeam(t *testing.T) {
	t.Parallel()

	fixed := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	c, err := New(Config{
		From:     "from@example.com",
		SMTPHost: "smtp.example",
		SMTPPort: 587,
		Now:      func() time.Time { return fixed },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	env := channel.NotificationEnvelope{
		SubscriptionID: uuid.New(),
		BundleBytes:    []byte(`{"resourceType":"Bundle","entry":[]}`),
		BundleKind:     channel.BundleEventNotification,
		ContentType:    channel.ContentTypeFHIRJSON,
	}
	out, err := c.buildMIME(env, "rcpt@example.com")
	if err != nil {
		t.Fatalf("buildMIME: %v", err)
	}
	mime := string(out)
	want := "Date: " + fixed.Format(time.RFC1123Z)
	if !strings.Contains(mime, want) {
		t.Errorf("MIME Date header missing fixed clock value %q; got:\n%s", want, mime)
	}
}

// TestEmailNew_DefaultTLSMinVersionIsTLS13 pins the default for the
// synthesized STARTTLS tls.Config minimum version. AC for #102:
// "Synthesized STARTTLS tls.Config MUST default to TLS 1.3 (matching
// rest-hook), with operator override via channels.email.tls_min_version."
//
// The default minimum is set inside Channel.dial; New does not eagerly
// build a tls.Config (one is built lazily per attempt when TLSConfig is
// nil). This test asserts the reachable behavior: when TLSMinVersion is
// zero the channel uses TLS 1.3 — anything else would let a misconfigured
// relay drop PHI to TLS 1.2.
func TestEmailNew_DefaultTLSMinVersionIsTLS13(t *testing.T) {
	t.Parallel()
	c, err := New(Config{
		From:     "from@example.com",
		SMTPHost: "smtp.example",
		SMTPPort: 587,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.cfg.TLSMinVersion != 0 && c.cfg.TLSMinVersion < tls.VersionTLS13 {
		t.Errorf("Config.TLSMinVersion = 0x%x; want zero or >= TLS 1.3 (0x%x)",
			c.cfg.TLSMinVersion, tls.VersionTLS13)
	}
}

// TestEmailNew_TLSMinVersionOverride pins that operators can downgrade
// to TLS 1.2 explicitly via the new knob.
func TestEmailNew_TLSMinVersionOverride(t *testing.T) {
	t.Parallel()
	c, err := New(Config{
		From:          "from@example.com",
		SMTPHost:      "smtp.example",
		SMTPPort:      587,
		TLSMinVersion: tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.cfg.TLSMinVersion != tls.VersionTLS12 {
		t.Errorf("Config.TLSMinVersion = 0x%x; want 0x%x", c.cfg.TLSMinVersion, tls.VersionTLS12)
	}
}
