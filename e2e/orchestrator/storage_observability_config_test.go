// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	effectivestore "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/effective_store"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/loader"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/redaction"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/secrets"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/correlation"
)

// TestE2E_S13_RedactionDepthCapDoesNotPanic exercises the redaction
// walker against a deeply nested payload that previously could
// stack-overflow. S-15 #3.
func TestE2E_S15_RedactionDepthCapDoesNotPanic(t *testing.T) {
	t.Parallel()
	tree := map[string]interface{}{}
	cur := tree
	for i := 0; i < redaction.MaxRedactDepth+200; i++ {
		next := map[string]interface{}{}
		cur["k"] = next
		cur = next
	}
	cur["leaf"] = "secret"

	m := redaction.NewMap()
	out := m.Redact(tree, "")
	if out == nil {
		t.Fatal("expected non-nil result")
	}
}

// TestE2E_S15_SecretFileSizeCap proves the boot path refuses to read a
// pathological ${file:...} secret. S-15 #4.
func TestE2E_S15_SecretFileSizeCap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	huge := filepath.Join(dir, "huge.bin")
	if err := os.WriteFile(huge, make([]byte, secrets.MaxSecretFileSize+10), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	tree := map[string]interface{}{
		"some_token": "${file:" + huge + "}",
	}
	_, _, _, err := secrets.ResolveWithFilePaths(tree, redaction.NewMap())
	if !errors.Is(err, secrets.ErrSecretFileTooLarge) {
		t.Fatalf("expected ErrSecretFileTooLarge; got %v", err)
	}
}

// TestE2E_S15_EnvVarCollisionsDetected verifies the boot loader
// surfaces ambiguous env-var derivations to the operator. S-15 #2.
func TestE2E_S15_EnvVarCollisionsDetected(t *testing.T) {
	t.Parallel()
	known := []string{
		"auth.trusted_issuers.0.jwks_url",
		"auth.trusted_issuers_0.jwks_url",
	}
	got := loader.EnvCollisions(known)
	if len(got) != 1 || got[0] != "AUTH_TRUSTED_ISSUERS_0_JWKS_URL" {
		t.Fatalf("expected AUTH_TRUSTED_ISSUERS_0_JWKS_URL collision; got %v", got)
	}
}

// TestE2E_S14_CorrelationIDRejectsHostileHeaders pins the public
// correlation HTTP header validator: CRLF, oversize, and disallowed
// chars all fall back to a fresh UUID. S-14 #6.
func TestE2E_S14_CorrelationIDRejectsHostileHeaders(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		header  string
		wantNew bool
	}{
		{"empty", "", true},
		{"crlf", "abc\r\nX-Smuggle: 1", true},
		{"non_uuid_oversize", strings.Repeat("a", 1024), true},
		{"valid_uuid", "11111111-1111-4111-8111-111111111111", false},
		// Under strict UUID-only validation (S-2.17), legacy non-UUID
		// shapes like "ord-2026.06.18-abc" are also rejected.
		{"non_uuid_dotted", "ord-2026.06.18-abc", true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := http.Header{}
			if c.header != "" {
				h.Set("X-Correlation-ID", c.header)
			}
			ctx := correlation.ExtractFromHeaders(context.Background(), h)
			got := correlation.IDFromContext(ctx)
			if c.wantNew {
				if got == c.header {
					t.Fatalf("expected fresh id; got original %q", got)
				}
				if strings.Contains(got, "\r") || strings.Contains(got, "\n") {
					t.Fatalf("got CRLF in id: %q", got)
				}
			} else if got != c.header {
				t.Fatalf("expected %q; got %q", c.header, got)
			}
		})
	}
}

// TestE2E_S15_PanicSubscriberDoesNotKillStore exercises the panic
// recover + bounded pool fix in effectivestore. S-15 #5.
func TestE2E_S15_PanicSubscriberDoesNotKillStore(t *testing.T) {
	t.Parallel()
	s := effectivestore.New()
	var fired atomic.Int32
	var wg sync.WaitGroup
	wg.Add(1)

	s.Subscribe("a", func(_ *effectivestore.Effective) { panic("boom") })
	s.Subscribe("a", func(_ *effectivestore.Effective) {
		if fired.Add(1) == 1 {
			wg.Done()
		}
	})

	s.Publish(&effectivestore.Effective{Tree: map[string]interface{}{"k": 1}})

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("non-panicking subscriber never fired (dispatcher killed by panic)")
	}
}
