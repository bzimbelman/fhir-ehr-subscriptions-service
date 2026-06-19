// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
)

// TestHarness_NoStubChannelActivators_StaticAudit walks every Go source
// file in this package and fails if it finds any identifier whose name
// matches stub* and refers to a channel activator.
//
// This is the OP #147 acceptance gate: "drop stubChannelActivator from
// e2e/harness/api.go" plus "harness MUST register real activators". The
// test runs without a Postgres pool so it executes in every CI lane,
// not just the e2e build tag.
func TestHarness_NoStubChannelActivators_StaticAudit(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	bad := []string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		fileAST, err := parser.ParseFile(fset, f, src, parser.AllErrors)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		ast.Inspect(fileAST, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			lower := strings.ToLower(id.Name)
			if !strings.HasPrefix(lower, "stub") {
				return true
			}
			// Allow stub* names if they're not channel-activator-related.
			// The point is no stubChannelActivator (or similar) hides a
			// no-op handshake. Adapter / pipeline stubs not relevant here
			// keep their names. Filter conservatively: any stub* name
			// containing "activat" or "channel" trips the check.
			if strings.Contains(lower, "activat") || strings.Contains(lower, "channel") {
				bad = append(bad, f+":"+id.Name)
			}
			return true
		})
	}
	if len(bad) > 0 {
		t.Fatalf("harness package still contains stub channel activators (OP #147 forbids these): %v", bad)
	}
}

// TestHarness_DefaultChannels_HasAllThreeRealActivators asserts that the
// harness's default channel registry registers a non-stub activator for
// each of: rest-hook, websocket, email. Each activator must be a real
// implementation (not the stub HandshakeSucceeded-everything activator).
//
// The test calls defaultChannels — a package-private helper Phase B
// adds — and checks the registered types via reflection.
func TestHarness_DefaultChannels_HasAllThreeRealActivators(t *testing.T) {
	subscriber := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(subscriber.Close)

	smtp, err := startTestSMTPRelay(t)
	if err != nil {
		t.Fatalf("startTestSMTPRelay: %v", err)
	}
	t.Cleanup(func() { _ = smtp.Close() })

	wsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))
	t.Cleanup(wsSrv.Close)

	reg, err := defaultChannels(channelsConfig{
		RestHookProbeURL:  subscriber.URL,
		SMTPHost:          smtp.Host(),
		SMTPPort:          smtp.Port(),
		WebsocketProbeURL: strings.Replace(wsSrv.URL, "http://", "ws://", 1) + "/ws/subscriptions",
	})
	if err != nil {
		t.Fatalf("defaultChannels: %v", err)
	}

	for _, name := range []string{"rest-hook", "websocket", "email"} {
		act, ok := reg[name]
		if !ok {
			t.Errorf("registry missing %q activator", name)
			continue
		}
		typ := reflect.TypeOf(act)
		typName := strings.ToLower(typ.String())
		if strings.Contains(typName, "stub") {
			t.Errorf("registry %q has stub-typed activator %s; OP #147 requires a real activator", name, typ)
		}
	}
}

// TestHarness_StartAPIServer_DoesNotRegisterStubByName asserts that
// StartAPIServer's resulting registry, accessed via the harness's
// channelsForTesting hook, contains no stub* type names.
//
// Because StartAPIServer needs a Pool, this test uses the harness's
// dependency-injected channelsForTesting helper that Phase B exposes
// (returns the would-be registry without actually starting the server).
func TestHarness_StartAPIServer_DoesNotRegisterStubByName(t *testing.T) {
	subscriber := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(subscriber.Close)
	smtp, err := startTestSMTPRelay(t)
	if err != nil {
		t.Fatalf("startTestSMTPRelay: %v", err)
	}
	t.Cleanup(func() { _ = smtp.Close() })

	cfg := APIServerConfig{
		RestHookProbeURL:  subscriber.URL,
		SMTPHost:          smtp.Host(),
		SMTPPort:          smtp.Port(),
		WebsocketProbeURL: "ws://127.0.0.1:9/ignored", // unused for static check
	}
	reg, err := channelsForTesting(context.Background(), cfg)
	if err != nil {
		t.Fatalf("channelsForTesting: %v", err)
	}
	for name, act := range reg {
		typName := strings.ToLower(reflect.TypeOf(act).String())
		if strings.Contains(typName, "stub") {
			t.Errorf("channel %q has stub-typed activator %s", name, reflect.TypeOf(act))
		}
	}
	// Each of the three required channel types must be wired.
	for _, name := range []string{"rest-hook", "websocket", "email"} {
		if _, ok := reg[name]; !ok {
			t.Errorf("required channel %q missing from registry", name)
		}
	}
	// Sanity: registry must be a handlers.ChannelRegistry.
	var _ handlers.ChannelRegistry = reg
}
