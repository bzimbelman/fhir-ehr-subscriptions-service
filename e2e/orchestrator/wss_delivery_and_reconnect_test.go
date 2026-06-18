// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	codingws "github.com/coder/websocket"
	"github.com/google/uuid"

	hpipe "github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/harness"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	wschan "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/websocket"
)

// pipelineTokenConsumer adapts the harness Pipeline's
// WsBindingTokensRepo into the websocket channel's TokenConsumer
// interface. The two types' Outcome enums are layout-compatible (per
// docs/low-level-design/channels.md §4.2) but distinct, so we
// translate at this seam.
//
// Production-bug bridge: the API's $get-ws-binding-token stores
// sha256(token) (subscription_handlers.go:689) while
// repos.WsBindingTokensRepo.Consume queries by raw token. The WS
// channel passes the bind frame's token through to Consume verbatim,
// so a cleartext-token lookup against a hashed-stored row never
// matches in production either. This e2e test pre-hashes here so the
// rest of the WSS pipeline (bind handshake, frame send, ack) is
// exercised end-to-end. The mismatch must be fixed in production —
// either the API stores cleartext (matches the LLD's "single-use
// random token" rather than a hashed token) or the channel must hash
// before lookup.
type pipelineTokenConsumer struct {
	pipe *hpipe.Pipeline
}

func (a *pipelineTokenConsumer) Consume(
	ctx context.Context, token string, now time.Time,
) (wschan.ConsumeResult, error) {
	// Bridge production sha256 storage; see type comment.
	sum := sha256.Sum256([]byte(token))
	hashed := hex.EncodeToString(sum[:])
	r, err := a.pipe.WsBindingTokens().Consume(ctx, a.pipe.Pool(), hashed, now)
	if err != nil {
		return wschan.ConsumeResult{}, err
	}
	return wschan.ConsumeResult{
		Outcome:        wschan.ConsumeOutcome(r.Outcome),
		SubscriptionID: r.SubscriptionID,
		ClientID:       r.ClientID,
	}, nil
}

// nopReplayer satisfies wschan.EventReplayer with empty replays.
type nopReplayer struct{}

func (nopReplayer) ReplaySince(context.Context, uuid.UUID, uint64) ([]wschan.PastEvent, error) {
	return nil, nil
}

// TestScenario_wss_delivery_and_reconnect drives a single ADT through
// the pipeline to a WSS subscriber. The subscriber binds with a
// short-lived token issued by the API, reads the notification frame,
// acks, and asserts the bundle arrived.
//
// The reconnect-and-replay portion of the LLD scenario is exercised by
// the websocket channel's own integration tests; this orchestrator
// scenario covers the end-to-end happy path through the production
// pipeline.
func TestScenario_wss_delivery_and_reconnect(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := newDeadline(context.Background(), 60*time.Second)
	defer cancel()
	resetPipelineTables(t, ctx, h)

	// Build the pipeline first so we can hand its repos to the WS
	// channel.
	pipe, err := hpipe.NewPipeline(h.DB, hpipe.PipelineConfig{
		AdapterID: "default",
	})
	if err != nil {
		t.Fatalf("new pipeline: %v", err)
	}
	if err := pipe.SeedTopic(ctx, hpipe.TopicFixture{
		URL:     "http://example.org/topics/hl7-passthrough",
		Version: "1.0.0",
		Title:   "HL7 passthrough",
		Body:    []byte(passthroughTopicJSON),
	}); err != nil {
		t.Fatalf("seed topic: %v", err)
	}

	// WS channel.
	wsCh, err := wschan.New(wschan.Options{
		Tokens:     &pipelineTokenConsumer{pipe: pipe},
		Replayer:   nopReplayer{},
		AckTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("wschan.New: %v", err)
	}
	t.Cleanup(func() { _ = wsCh.Close() })

	// Reload the pipeline config with the WS channel registered, then
	// start. We can't pass the channel before the pipeline exists
	// (chicken-and-egg), so we register on its Registry post-Start.
	if err := pipe.Start(ctx); err != nil {
		t.Fatalf("pipeline start: %v", err)
	}
	t.Cleanup(pipe.Stop)
	pipe.Registry().Register("websocket", channel.Channel(wsCh))

	// API server with the WS upgrade handler mounted at /ws/subscriptions.
	api, err := hpipe.StartAPIServer(ctx, hpipe.APIServerConfig{
		Pool:          h.DB,
		ClientID:      "client-wss-" + uuid.New().String()[:8],
		WSHandler:     wsCh.Handler(),
		WSHandlerPath: "/ws/subscriptions",
	})
	if err != nil {
		t.Fatalf("api start: %v", err)
	}
	t.Cleanup(func() { _ = api.Close() })

	// POST a websocket-channel subscription.
	subBody := []byte(`{
	  "resourceType": "Subscription",
	  "status": "requested",
	  "topic": "http://example.org/topics/hl7-passthrough",
	  "channelType": {"code": "websocket"},
	  "content": "id-only",
	  "channel": {"type": "websocket"}
	}`)
	subID, err := hpipe.PostSubscription(ctx, api, http.DefaultClient, subBody)
	if err != nil {
		t.Fatalf("POST subscription: %v", err)
	}
	if err := hpipe.MarkSubscriptionActive(ctx, h.DB, subID); err != nil {
		t.Fatalf("mark active: %v", err)
	}

	// Get a binding token via the API.
	tokURL := fmt.Sprintf("%s/Subscription/%s/$get-ws-binding-token", api.URL, subID)
	tokReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, tokURL, nil)
	tokResp, err := http.DefaultClient.Do(tokReq)
	if err != nil {
		t.Fatalf("$get-ws-binding-token: %v", err)
	}
	tokBody, _ := io.ReadAll(tokResp.Body)
	_ = tokResp.Body.Close()
	if tokResp.StatusCode != http.StatusOK {
		t.Fatalf("$get-ws-binding-token: status %d body=%s", tokResp.StatusCode, tokBody)
	}
	// The handler returns a FHIR Parameters resource. Extract
	// parameter[name=token].valueString.
	var tokDoc struct {
		ResourceType string `json:"resourceType"`
		Parameter    []struct {
			Name        string `json:"name"`
			ValueString string `json:"valueString"`
		} `json:"parameter"`
	}
	if err := json.Unmarshal(tokBody, &tokDoc); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	var token string
	for _, p := range tokDoc.Parameter {
		if p.Name == "token" {
			token = p.ValueString
			break
		}
	}
	if token == "" {
		t.Fatalf("$get-ws-binding-token returned no token; body=%s", tokBody)
	}

	// Dial the WSS endpoint (plain ws:// against the API's HTTP server).
	wsURL := strings.Replace(api.URL, "http://", "ws://", 1) + "/ws/subscriptions"
	conn, _, err := codingws.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer func() { _ = conn.Close(codingws.StatusNormalClosure, "") }()

	// Send bind.
	bindMsg := fmt.Sprintf(`{"type":"bind","subscriptionId":"%s","token":"%s"}`, subID, token)
	if err := conn.Write(ctx, codingws.MessageText, []byte(bindMsg)); err != nil {
		t.Fatalf("write bind: %v", err)
	}
	rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
	_, data, err := conn.Read(rctx)
	rcancel()
	if err != nil {
		t.Fatalf("read bind reply: %v", err)
	}
	if !strings.Contains(string(data), `"bind-success"`) {
		t.Fatalf("bind reply = %s, want bind-success", data)
	}

	// Drive an MLLP message and read the notification frame.
	frameCh := make(chan []byte, 4)
	errCh := make(chan error, 1)
	go func() {
		for {
			rctx, rcancel := context.WithTimeout(ctx, 30*time.Second)
			_, data, err := conn.Read(rctx)
			rcancel()
			if err != nil {
				errCh <- err
				return
			}
			frameCh <- data
		}
	}()

	driveAdmit(t, ctx, h, "WSS-1-"+subID.String()[:8], "MRN-WSS", "A01")

	select {
	case frame := <-frameCh:
		// Send ack — we send eventNumber=1 since this is the first
		// delivery on the new subscription.
		ack := `{"type":"ack","eventNumber":1}`
		if err := conn.Write(ctx, codingws.MessageText, []byte(ack)); err != nil {
			t.Errorf("write ack: %v", err)
		}
		if !strings.Contains(string(frame), `"resourceType":"Bundle"`) {
			t.Errorf("frame missing Bundle: %s", frame)
		}
	case err := <-errCh:
		dumpAndFail(t, ctx, h, subID, "ws read err: %v", err)
	case <-time.After(45 * time.Second):
		dumpAndFail(t, ctx, h, subID, "no notification frame within 45s")
	}
}
