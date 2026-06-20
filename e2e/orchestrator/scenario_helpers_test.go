// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	hpipe "github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/harness"
)

// scenarioFixture bundles the per-scenario in-process plumbing so each
// test file can stand the world up in three lines instead of fifty.
//
// The lifecycle:
//
//	fx := newScenarioFixture(t, ctx, scenarioConfig{...})
//	defer fx.cleanup()  // (registered as t.Cleanup)
//	... drive the scenario ...
type scenarioFixture struct {
	tlsRest *hpipe.TLSRestHookServer
	pipe    *hpipe.Pipeline
	api     *hpipe.APIServer
}

// API returns the per-scenario API server handle.
func (fx *scenarioFixture) API() *hpipe.APIServer { return fx.api }

// scenarioConfig parameterizes newScenarioFixture.
type scenarioConfig struct {
	pipelineConfig hpipe.PipelineConfig
	topics         []hpipe.TopicFixture
	clientID       string
	// preBuiltTLS allows scenarios that need to register the rest-hook
	// channel via their own TLS server (e.g., backpressure, which
	// configures a 503-then-200 handler) to share that TLS server with
	// the fixture instead of having the fixture build a fresh one.
	preBuiltTLS *hpipe.TLSRestHookServer
}

// newScenarioFixture starts a TLS rest-hook receiver wrapping the
// shared mocksub.RestHookReceiver, builds and starts a Pipeline with
// the given config (defaulting to the rest-hook channel pointed at the
// TLS listener), seeds the supplied topics, and brings up an API
// server for POST /Subscription. Caller-controlled additions to
// pipelineConfig.Channels are honored verbatim.
func newScenarioFixture(t *testing.T, ctx context.Context, h *Harness, cfg scenarioConfig) *scenarioFixture {
	t.Helper()
	// Truncate every pipeline-touching table so a scenario starts from
	// a clean slate. The test package shares one Postgres container
	// across all tests; without this reset, pending deliveries and
	// inactive subscriptions from earlier scenarios contaminate later
	// scenarios' workers.
	resetPipelineTables(t, ctx, h)
	fx := &scenarioFixture{}

	if cfg.preBuiltTLS != nil {
		fx.tlsRest = cfg.preBuiltTLS
	} else {
		tlsSrv, err := hpipe.StartTLSRestHookServer(h.MockSub.RestHook.Handler())
		if err != nil {
			t.Fatalf("scenario: tls rest-hook: %v", err)
		}
		fx.tlsRest = tlsSrv
		t.Cleanup(func() { _ = tlsSrv.Close() })
	}

	// Pipeline.
	pipe, err := hpipe.NewPipeline(h.DB, cfg.pipelineConfig)
	if err != nil {
		t.Fatalf("scenario: new pipeline: %v", err)
	}
	for _, topic := range cfg.topics {
		if err := pipe.SeedTopic(ctx, topic); err != nil {
			t.Fatalf("scenario: seed topic %s: %v", topic.URL, err)
		}
	}
	if err := pipe.Start(ctx); err != nil {
		t.Fatalf("scenario: pipeline start: %v", err)
	}
	fx.pipe = pipe
	t.Cleanup(pipe.Stop)

	// API. OP #327: plumb the harness's mocksub SMTP + websocket probe
	// targets through to StartAPIServer so the email and websocket
	// activators handshake against real localhost dependencies instead
	// of falling through to failClosedActivator. The rest-hook channel
	// is left to StartAPIServer's auto-spin-up sink (api.go), which
	// routes handshakes through a localhost 200-OK server so they don't
	// pollute the mocksub journal that filter-drop scenarios assert on.
	//
	// OP #334: opt in to RestHookProbeOnlyHandshake so the activator
	// posts handshakes to the auto-spun probe sink ALWAYS, even when
	// the subscription row's endpoint points at the mocksub /hook/<tag>
	// path. Without this, filter-drop's per-tag journal-empty assertion
	// would see the handshake POST and fail. (The default activator
	// behavior — prefer row.Endpoint — is restored for the helpers
	// tests that explicitly set RestHookProbeURL to the mocksub root,
	// which only journals POSTs landing at /hook/<id>.)
	clientID := cfg.clientID
	if clientID == "" {
		clientID = "client-" + uuid.New().String()[:8]
	}
	smtpHost, smtpPort := mockSubSMTPHostPort(t, h)
	api, err := hpipe.StartAPIServer(ctx, hpipe.APIServerConfig{
		Pool:                       h.DB,
		ClientID:                   clientID,
		SMTPHost:                   smtpHost,
		SMTPPort:                   smtpPort,
		WebsocketProbeURL:          "ws://" + h.MockSub.HTTPAddr + "/ws/subscriptions",
		RestHookProbeOnlyHandshake: true,
	})
	if err != nil {
		t.Fatalf("scenario: api: %v", err)
	}
	fx.api = api
	t.Cleanup(func() { _ = api.Close() })

	return fx
}

// pipeline returns the per-scenario Pipeline.
func (fx *scenarioFixture) pipeline() *hpipe.Pipeline { return fx.pipe }

// mockSubSMTPHostPort splits the harness's FakeSMTP listen address into
// host + port. Failures are unexpected — the FakeSMTP listener is
// always bound to 127.0.0.1:<ephemeral>.
func mockSubSMTPHostPort(t *testing.T, h *Harness) (string, int) {
	t.Helper()
	if h == nil || h.MockSub == nil || h.MockSub.SMTP == nil {
		t.Fatalf("scenario: harness mocksub SMTP not initialized")
	}
	host, portStr, err := net.SplitHostPort(h.MockSub.SMTP.Addr().String())
	if err != nil {
		t.Fatalf("scenario: split smtp addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("scenario: parse smtp port: %v", err)
	}
	return host, port
}

// createSubscription POSTs a Subscription via the API and synchronously
// flips its status to active so the submatcher's claim sees it without
// racing the activation goroutine.
func (fx *scenarioFixture) createSubscription(
	ctx context.Context, t *testing.T, h *Harness, body []byte,
) uuid.UUID {
	t.Helper()
	id, err := hpipe.PostSubscription(ctx, fx.api, fx.api.Client(), body)
	if err != nil {
		t.Fatalf("POST /Subscription: %v", err)
	}
	if err := hpipe.MarkSubscriptionActive(ctx, h.DB, id); err != nil {
		t.Fatalf("mark active: %v", err)
	}
	return id
}

// restHookSub builds a JSON Subscription resource pointing at
// https://<tlsURL>/hook/<tag>. The tag is what mocksub journals as
// SubscriptionID, so WaitForNotification(..., tag, ...) finds it.
func restHookSub(topic, tlsURL, tag string, filterBy []map[string]any) []byte {
	endpoint := fmt.Sprintf("%s/hook/%s", tlsURL, tag)
	body := map[string]any{
		"resourceType": "Subscription",
		"status":       "requested",
		"topic":        topic,
		"channelType":  map[string]any{"code": "rest-hook"},
		"endpoint":     endpoint,
		"content":      "full-resource",
		"channel":      map[string]any{"type": "rest-hook", "endpoint": endpoint},
	}
	if len(filterBy) > 0 {
		body["filterBy"] = filterBy
	}
	b, _ := json.Marshal(body)
	return b
}

// driveAdmit posts /scenarios/admit_patient with deterministic fields.
func driveAdmit(t *testing.T, ctx context.Context, h *Harness, messageID, patientID, trigger string) {
	t.Helper()
	postScenario(t, ctx, h, "/scenarios/admit_patient", map[string]any{
		"patient_id":  patientID,
		"message_id":  messageID,
		"trigger":     trigger,
		"family_name": "Doe",
		"given_name":  "Jane",
	})
}

// dumpAndFail logs pipeline state and fails the test with msg.
func dumpAndFail(t *testing.T, ctx context.Context, h *Harness, subID uuid.UUID, format string, args ...any) {
	t.Helper()
	dumpPipelineState(t, ctx, h, subID)
	t.Fatalf(format, args...)
}

// shortTag returns a per-test unique tag suitable for use as a
// subscription identifier in the mocksub journal.
func shortTag(prefix string) string {
	return prefix + "-" + uuid.New().String()[:8]
}

// newDeadline returns ctx scoped to a per-scenario deadline.
func newDeadline(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
}
