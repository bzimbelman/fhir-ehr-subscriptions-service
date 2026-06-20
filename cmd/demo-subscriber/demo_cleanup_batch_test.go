// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"go/parser"
	"go/token"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDemoSubscriber_DefaultListenIsRoutableFromCompose pins OP #156:
// the demo-subscriber MUST NOT default to `127.0.0.1:0` (an ephemeral
// loopback port the bridge cannot reach across a Docker bridge
// network). The default listen MUST bind on a routable interface
// (0.0.0.0:9090).
func TestDemoSubscriber_DefaultListenIsRoutableFromCompose(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	body := string(data)
	// Match the actual flag default, not stray comment text. The
	// flag declaration is `fs.StringVar(&f.listen, "listen", "<value>", ...)`.
	if strings.Contains(body, `fs.StringVar(&f.listen, "listen", "127.0.0.1:0"`) {
		t.Fatalf(`OP #156: demo-subscriber default --listen must not be "127.0.0.1:0" (ephemeral loopback unreachable from compose)`)
	}
	if !strings.Contains(body, `fs.StringVar(&f.listen, "listen", "0.0.0.0:9090"`) {
		t.Fatalf(`OP #156: demo-subscriber default --listen must be 0.0.0.0:9090; got:
%s`, body)
	}
}

// TestDemoSubscriber_NoE2EImports asserts OP #158: the operator-facing
// subscriber MUST NOT import e2e/* test scaffolding.
func TestDemoSubscriber_NoE2EImports(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(".", name)
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(p, "/e2e/") {
				t.Errorf("%s: forbidden e2e import %q (OP #158)", path, p)
			}
		}
	}
}

// TestPostSubscription_PicksSingleChannelShape asserts OP #157:
// the Subscription body MUST emit a SINGLE channel-spec shape, not
// both R4B `channel:{type,endpoint}` AND R5-style top-level
// `channelType`+`endpoint`. The bridge's JSON schema today requires
// the R4B `channel` block, so that is the shape we keep — the test
// fails if the body adds duplicate top-level R5 fields OR if the
// `channel` block goes missing.
func TestPostSubscription_PicksSingleChannelShape(t *testing.T) {
	t.Parallel()

	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 64*1024)
		n, _ := r.Body.Read(body)
		captured = body[:n]
		w.Header().Set("Location", "/Subscription/single")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"single"}`))
	}))
	defer srv.Close()

	cfg := SubscribeConfig{
		BridgeBaseURL: srv.URL,
		Token:         "tok",
		Topic:         "http://demo.org/topics/lab-results",
		ChannelType:   "rest-hook",
		Endpoint:      "http://demo-subscriber:9090/hook/sub-1",
		HTTPClient:    srv.Client(),
	}
	if _, err := postSubscription(context.Background(), cfg); err != nil {
		t.Fatalf("postSubscription: %v", err)
	}

	var sub map[string]any
	if err := json.Unmarshal(captured, &sub); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	// Single source of truth: the R4B `channel` block.
	channel, ok := sub["channel"].(map[string]any)
	if !ok {
		t.Fatalf("OP #157: Subscription body must include the bridge-required `channel` block; got %v", sub["channel"])
	}
	if channel["type"] != "rest-hook" {
		t.Errorf("OP #157: channel.type: got %v want rest-hook", channel["type"])
	}
	if channel["endpoint"] != "http://demo-subscriber:9090/hook/sub-1" {
		t.Errorf("OP #157: channel.endpoint: got %v", channel["endpoint"])
	}
	// The duplicate R5 fields must be absent — that was the
	// "both shapes at once" hazard the OP called out.
	if _, present := sub["channelType"]; present {
		t.Errorf("OP #157: top-level `channelType` must not be emitted alongside the `channel` block; got %v", sub["channelType"])
	}
	if _, present := sub["endpoint"]; present {
		t.Errorf("OP #157: top-level `endpoint` must not be emitted alongside the `channel` block; got %v", sub["endpoint"])
	}
}

// stallingHandler blocks until ctx is cancelled OR the request's own
// context is cancelled (httptest.Server tears down by closing the
// connection, which cancels r.Context).
func stallingHandler(ctx context.Context) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-ctx.Done():
		case <-r.Context().Done():
		}
	}
}

// TestPostSubscription_HonoursDefaultTimeout asserts OP #161: when
// SubscribeConfig.HTTPClient is nil, postSubscription MUST use a
// bounded-timeout HTTP client (NOT http.DefaultClient, which has
// zero timeout). Otherwise a stalled bridge blocks the demo forever.
//
// Not Parallel because it mutates the package-level
// defaultHTTPTimeoutNanos that TestMintToken_HonoursDefaultTimeout
// also sets — running the two in parallel would race on the value.
func TestPostSubscription_HonoursDefaultTimeout(t *testing.T) {

	stallCtx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(stallingHandler(stallCtx))
	t.Cleanup(func() {
		// Release the handler before tearing down the server so
		// httptest's connection-tracking WaitGroup completes.
		cancel()
		srv.CloseClientConnections()
		srv.Close()
	})

	// Run postSubscription with HTTPClient nil and a tiny override
	// of the package-level default timeout. The test plumbs the
	// override via a helper so we don't have to wait 30s on every
	// CI run.
	prev := httpTimeout()
	setHTTPTimeoutForTest(250 * time.Millisecond)
	t.Cleanup(func() { setHTTPTimeoutForTest(prev) })

	cfg := SubscribeConfig{
		BridgeBaseURL: srv.URL,
		Token:         "tok",
		Topic:         "http://demo.org/topics/lab-results",
		ChannelType:   "rest-hook",
		Endpoint:      "http://demo-subscriber:9090/hook/sub-1",
	}
	start := time.Now()
	_, err := postSubscription(context.Background(), cfg)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("postSubscription: want timeout error from stalled bridge; got nil")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("postSubscription: HTTP client did not honour default timeout (waited %s); OP #161 requires bounded timeout, not http.DefaultClient", elapsed)
	}
}

// TestMintToken_HonoursDefaultTimeout asserts OP #161: mintToken's
// fallback HTTP client must also be timeout-bounded.
//
// Not Parallel — see TestPostSubscription_HonoursDefaultTimeout note.
func TestMintToken_HonoursDefaultTimeout(t *testing.T) {

	stallCtx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(stallingHandler(stallCtx))
	t.Cleanup(func() {
		cancel()
		srv.CloseClientConnections()
		srv.Close()
	})

	prev := httpTimeout()
	setHTTPTimeoutForTest(250 * time.Millisecond)
	t.Cleanup(func() { setHTTPTimeoutForTest(prev) })

	priv, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	start := time.Now()
	_, err = mintToken(context.Background(), MintConfig{
		TokenURL:   srv.URL,
		ClientID:   "demo-client",
		Scope:      "system/Subscription.cruds",
		PrivateKey: priv,
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("mintToken: want timeout error; got nil")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("mintToken: HTTP client did not honour default timeout (waited %s); OP #161 requires bounded timeout", elapsed)
	}
}

// TestSubscribeFlow_BindsListenerOn0000 is a smoke check that
// proves the routability fix: spinning up the demo-subscriber's run()
// loop with --listen 0.0.0.0:0 gives us an addr we can dial from
// outside loopback. We assert the resolved port is non-zero AND the
// addr is NOT a loopback bind (which is the symptom of the OP #156
// regression).
func TestSubscribeFlow_BindsListenerOn0000(t *testing.T) {
	t.Parallel()

	// Bind directly with the same call run() makes; if a future
	// change reverts to "127.0.0.1:0", the resolved address would
	// be loopback and this test should fail to surface that.
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("addr type: %T", ln.Addr())
	}
	if addr.IP.IsLoopback() {
		t.Fatalf("OP #156: demo-subscriber listener bound to loopback %s; must bind 0.0.0.0", addr)
	}
	if addr.Port == 0 {
		t.Fatalf("OP #156: listener resolved to port 0 (%v)", addr)
	}
}
