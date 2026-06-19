// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	chemail "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/email"
	chmessage "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/message"
	chwebsocket "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/websocket"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/scheduler"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/lifecycle"
)

// Phase A — RED tests for stories #101 (websocket), #102 (email), #103
// (message) channel wiring into the production scheduler ChannelRegistry.
//
// These tests pin the wiring contract:
//
//   - ChannelsConfig MUST have Email and Message blocks parseable from
//     YAML (current main has only RestHook + WebSocket).
//   - WebSocketChannelConfig MUST surface every Options knob the
//     channel package exposes (BindTimeout, MaxFrameBytes, MaxSessions,
//     MaxSessionsPerClient, MaxReplayEvents, PingWriteTimeout,
//     UpgradeReadHeaderTimeout, SubprotocolBindPrefix).
//   - applySets MUST round-trip every channels.{websocket,email,message}.*
//     dotted key from a CLI override.
//   - The Channel SPI MUST expose Close() error so the lifecycle
//     module can drain in-flight bind handshakes.
//   - buildProductionRuntime MUST construct websocket/email/message
//     channels and Register them in chReg under the stable codes
//     "websocket", "email", "message".
//   - buildProductionRuntime MUST register a "channels.close" lifecycle
//     hook in PhaseCloseConnections so every Close() fires on graceful
//     shutdown.

// --- Phase A.1 — config shape / round-trip ---

// TestChannelsConfig_HasEmailAndMessageBlocks pins the new YAML keys
// channels.email.* and channels.message.*. RED today because
// ChannelsConfig only declares RestHook + WebSocket.
func TestChannelsConfig_HasEmailAndMessageBlocks(t *testing.T) {
	t.Parallel()

	v := reflect.TypeOf(ChannelsConfig{})
	if _, ok := v.FieldByName("Email"); !ok {
		t.Errorf("ChannelsConfig has no Email field; story #102 requires channels.email.* block")
	}
	if _, ok := v.FieldByName("Message"); !ok {
		t.Errorf("ChannelsConfig has no Message field; story #103 requires channels.message.* block")
	}
}

// TestWebSocketChannelConfig_SurfacesEveryOptionsKnob pins that every
// Options field the websocket channel package exposes round-trips
// from YAML. RED today: WebSocketChannelConfig has only OriginPatterns,
// IdleTimeout, PingInterval.
func TestWebSocketChannelConfig_SurfacesEveryOptionsKnob(t *testing.T) {
	t.Parallel()

	required := []string{
		"OriginPatterns",
		"IdleTimeout",
		"PingInterval",
		"BindTimeout",
		"MaxFrameBytes",
		"MaxSessions",
		"MaxSessionsPerClient",
		"MaxReplayEvents",
		"PingWriteTimeout",
		"UpgradeReadHeaderTimeout",
	}
	v := reflect.TypeOf(WebSocketChannelConfig{})
	for _, name := range required {
		if _, ok := v.FieldByName(name); !ok {
			t.Errorf("WebSocketChannelConfig missing field %s — story #101 requires every Options knob to round-trip from YAML", name)
		}
	}
}

// TestApplySets_ChannelsKeysRoundTrip pins applySets handles the new
// channels.{ws,email,message}.* dotted keys. RED today.
func TestApplySets_ChannelsKeysRoundTrip(t *testing.T) {
	t.Parallel()

	cfg := &Config{}
	sets := []string{
		"channels.email.smtp_host=smtp.example",
		"channels.email.smtp_port=587",
		"channels.email.from=noreply@example.com",
		"channels.message.user_agent=test-message/1.0",
		"channels.websocket.bind_timeout=10s",
		"channels.websocket.max_sessions=42",
	}
	if err := applySets(cfg, sets); err != nil {
		t.Fatalf("applySets: %v", err)
	}
}

// --- Phase A.2 — Channel SPI Close() ---

// TestChannelSPI_ClosePresent pins that channel.Channel exposes
// Close() error (story #101 SPI extension). RED today: the interface
// has only Deliver().
func TestChannelSPI_ClosePresent(t *testing.T) {
	t.Parallel()

	iface := reflect.TypeOf((*channel.Channel)(nil)).Elem()
	if _, ok := iface.MethodByName("Close"); !ok {
		t.Errorf("channel.Channel SPI missing Close() error — story #101 requires the lifecycle module to call Close() on every registered channel during shutdown")
	}
}

// --- Phase A.3 — production runtime wiring ---

// integrationCfg returns the minimum viable production config for
// buildProductionRuntime. After Phase B Channels.Email + Channels.Message
// are populated via applySets so this helper does not need to reference
// the new struct fields directly (reflection-based tests above pin the
// fields exist).
func integrationCfg(t *testing.T, dbURL string) *Config {
	t.Helper()
	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1", Environment: "test", Mode: DeploymentModeProbeOnly},
		Adapter:    AdapterConfig{ID: "default"},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: "127.0.0.1:0", ProbeBind: "127.0.0.1:0", Insecure: true}},
		Lifecycle:  LifecycleConfig{ShutdownGracePeriod: 5 * time.Second},
		Database:   DatabaseConfig{URL: dbURL},
		Codec: CodecConfig{
			ActiveKeyVersion: 1,
			Keys: []CodecKeySpec{
				{Version: 1, Material: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
			},
		},
		Channels: ChannelsConfig{
			RestHook: RestHookChannelConfig{RequestTimeout: 5 * time.Second},
			WebSocket: WebSocketChannelConfig{
				OriginPatterns: []string{"*.example.com"},
			},
		},
	}
	// Phase B adds these dotted keys; Phase A's other tests confirm
	// applySets accepts them.
	if err := applySets(cfg, []string{
		"channels.email.from=noreply@example.com",
		"channels.email.smtp_host=smtp.example",
		"channels.email.smtp_port=587",
	}); err != nil {
		t.Fatalf("applySets: %v", err)
	}
	return cfg
}

// TestProductionRuntime_RegistersAllThreeChannels pins that
// buildProductionRuntime constructs and registers websocket, email,
// and message channels under their canonical codes. RED today.
//
// Gated on TEST_PG_URL since the runtime requires Postgres.
func TestProductionRuntime_RegistersAllThreeChannels(t *testing.T) {
	dbURL := os.Getenv("TEST_PG_URL")
	if dbURL == "" {
		t.Skip("TEST_PG_URL not set; integration assertion runs in CI")
	}

	cfg := integrationCfg(t, dbURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lcMod, err := lifecycle.Start(ctx, lifecycle.LifecycleConfig{
		ShutdownGracePeriod: 5 * time.Second,
	}, lifecycle.LifecycleContext{Logger: logger})
	if err != nil {
		t.Fatalf("lifecycle.Start: %v", err)
	}
	rt, err := buildProductionRuntime(ctx, cfg, logger, lcMod)
	if err != nil {
		t.Fatalf("buildProductionRuntime: %v", err)
	}
	t.Cleanup(func() { rt.shutdown(context.Background()) })

	// Probe the runtime via reflection — Phase B adds a `chReg` field
	// (or equivalent) that exposes the registry to tests.
	v := reflect.ValueOf(rt).Elem()
	var reg scheduler.ChannelRegistry
	for _, candidate := range []string{"chReg", "channels", "channelRegistry"} {
		f := v.FieldByName(candidate)
		if !f.IsValid() {
			continue
		}
		if r, ok := f.Interface().(scheduler.ChannelRegistry); ok {
			reg = r
			break
		}
		if r, ok := f.Interface().(*scheduler.MapRegistry); ok {
			reg = r
			break
		}
	}
	if reg == nil {
		t.Fatalf("productionRuntime exposes no chReg-shaped field; Phase B must store the *scheduler.MapRegistry on the runtime so lifecycle and tests can reach it")
	}

	for _, code := range []string{"rest-hook", "websocket", "email", "message"} {
		ch, ok := reg.Lookup(code)
		if !ok {
			t.Errorf("scheduler.ChannelRegistry missing code %q — Phase B must wire it", code)
			continue
		}
		if ch == nil {
			t.Errorf("scheduler.ChannelRegistry[%q] is nil", code)
		}
	}
}

// TestProductionRuntime_ChannelsCloseHookRegistered pins the lifecycle
// hook that drains websocket / email / message channels on shutdown.
// RED today.
func TestProductionRuntime_ChannelsCloseHookRegistered(t *testing.T) {
	dbURL := os.Getenv("TEST_PG_URL")
	if dbURL == "" {
		t.Skip("TEST_PG_URL not set; integration assertion runs in CI")
	}

	cfg := integrationCfg(t, dbURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lcMod, err := lifecycle.Start(ctx, lifecycle.LifecycleConfig{
		ShutdownGracePeriod: 5 * time.Second,
	}, lifecycle.LifecycleContext{Logger: logger})
	if err != nil {
		t.Fatalf("lifecycle.Start: %v", err)
	}
	rt, err := buildProductionRuntime(ctx, cfg, logger, lcMod)
	if err != nil {
		t.Fatalf("buildProductionRuntime: %v", err)
	}
	t.Cleanup(func() { rt.shutdown(context.Background()) })

	want := "channels.close"
	found := false
	for _, name := range lcMod.RegisteredShutdownNames(lifecycle.PhaseCloseConnections) {
		if name == want {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("lifecycle hook %q not registered in PhaseCloseConnections; story #101 requires the scheduler to call Close on every registered channel during shutdown", want)
	}
}

// TestProductionRuntime_HandlersChannelRegistryHasAllThree pins that
// the API ChannelRegistry (used by handlers for activator dispatch)
// covers websocket, email, and message. RED today: the cmd wiring
// hardcodes only rest-hook + websocket + email — message is missing,
// so a POST /Subscription with channelType=message returns
// "unsupported channelType" 422.
func TestProductionRuntime_HandlersChannelRegistryHasAllThree(t *testing.T) {
	dbURL := os.Getenv("TEST_PG_URL")
	if dbURL == "" {
		t.Skip("TEST_PG_URL not set; integration assertion runs in CI")
	}

	cfg := integrationCfg(t, dbURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lcMod, err := lifecycle.Start(ctx, lifecycle.LifecycleConfig{
		ShutdownGracePeriod: 5 * time.Second,
	}, lifecycle.LifecycleContext{Logger: logger})
	if err != nil {
		t.Fatalf("lifecycle.Start: %v", err)
	}
	rt, err := buildProductionRuntime(ctx, cfg, logger, lcMod)
	if err != nil {
		t.Fatalf("buildProductionRuntime: %v", err)
	}
	t.Cleanup(func() { rt.shutdown(context.Background()) })

	// Probe a request to the metadata CapabilityStatement: the
	// extension renders the registered channel codes. After Phase B
	// the response includes "message" as a registered code.
	req := httptest.NewRequest(http.MethodGet, "/metadata", nil)
	rec := httptest.NewRecorder()
	rt.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/metadata: status %d; body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, code := range []string{"websocket", "email", "message"} {
		if !strings.Contains(body, code) {
			t.Errorf("CapabilityStatement does not advertise channel code %q (body=%q)", code, body)
		}
	}
	// Decode-touch so go vet doesn't flag json import as unused
	_ = json.Decoder{}
}

// --- Phase A.4 — channel package contracts the wiring depends on ---

// TestEmailConfig_HasNowSeam pins that email.Config exposes a Now
// clock seam. AC for #102: "MIME Date header MUST come through the
// channel's Now() clock seam." RED today.
func TestEmailConfig_HasNowSeam(t *testing.T) {
	t.Parallel()
	v := reflect.TypeOf(chemail.Config{})
	if _, ok := v.FieldByName("Now"); !ok {
		t.Errorf("email.Config missing Now func() time.Time — AC #102 requires deterministic Date header")
	}
}

// TestEmailConfig_HasTLSMinVersionOverride pins that operators can
// override the synthesized STARTTLS tls.Config minimum version. AC for
// #102: "Synthesized STARTTLS tls.Config MUST default to TLS 1.3,
// with operator override via channels.email.tls_min_version." RED
// today: email.Config has only TLSConfig (full tls.Config override),
// not a min-version knob.
func TestEmailConfig_HasTLSMinVersionOverride(t *testing.T) {
	t.Parallel()
	v := reflect.TypeOf(chemail.Config{})
	if _, ok := v.FieldByName("TLSMinVersion"); !ok {
		t.Errorf("email.Config missing TLSMinVersion uint16 — AC #102 requires a TLS minimum override")
	}
}

// TestMessageOptions_HasCloseShape exists so the package import
// is exercised even when other tests are skipped.
func TestMessageOptions_HasCloseShape(t *testing.T) {
	t.Parallel()
	c, err := chmessage.New(chmessage.Options{ServerEndpoint: "https://example/m"})
	if err != nil {
		t.Fatalf("message.New: %v", err)
	}
	// After Phase B every Channel implementation must expose Close().
	v := reflect.ValueOf(c)
	m := v.MethodByName("Close")
	if !m.IsValid() {
		t.Errorf("message.Channel missing Close() method — required by extended Channel SPI (#101)")
	}
}

// TestWebsocketChannel_CloseIdempotent pins existing behaviour so
// Phase B can rely on Close() being safe to call from the lifecycle
// hook even when the channel is already closed.
func TestWebsocketChannel_CloseIdempotent(t *testing.T) {
	t.Parallel()

	c, err := chwebsocket.New(chwebsocket.Options{
		Tokens:   stubTokenConsumer{},
		Replayer: stubReplayer{},
	})
	if err != nil {
		t.Fatalf("websocket.New: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close should be a no-op, got: %v", err)
	}
}

type stubTokenConsumer struct{}

func (stubTokenConsumer) Consume(_ context.Context, _ string, _ time.Time) (chwebsocket.ConsumeResult, error) {
	return chwebsocket.ConsumeResult{Outcome: chwebsocket.ConsumeNotFound}, nil
}

type stubReplayer struct{}

func (stubReplayer) ReplaySince(_ context.Context, _ uuid.UUID, _ uint64) ([]chwebsocket.PastEvent, error) {
	return nil, nil
}

var _ chwebsocket.EventReplayer = stubReplayer{}
var _ chwebsocket.TokenConsumer = stubTokenConsumer{}
