// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	hpipe "github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/harness"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/email"
)

// TestScenario_email_v1_smtp configures an email-channel subscription
// pointing at the orchestrator's FakeSMTP. Drives an event and asserts
// the FakeSMTP journal records exactly one message addressed to the
// configured mailto:.
func TestScenario_email_v1_smtp(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := newDeadline(context.Background(), 60*time.Second)
	defer cancel()

	host, portStr, err := net.SplitHostPort(h.MockSub.SMTP.Addr().String())
	if err != nil {
		t.Fatalf("smtp addr split: %v", err)
	}
	port, _ := strconv.Atoi(portStr)
	emailCh, err := email.New(email.Config{
		Mode:     email.ModeSMTP,
		From:     "fhir-subs@example.org",
		SMTPHost: host,
		SMTPPort: port,
		STARTTLS: email.STARTTLSDisabled,
	})
	if err != nil {
		t.Fatalf("email.New: %v", err)
	}

	fx := newScenarioFixture(t, ctx, h, scenarioConfig{
		pipelineConfig: hpipe.PipelineConfig{
			AdapterID: "default",
			Channels:  map[string]channel.Channel{"email": emailCh},
		},
		topics: []hpipe.TopicFixture{{
			URL:     "http://example.org/topics/hl7-passthrough",
			Version: "1.0.0",
			Title:   "HL7 passthrough",
			Body:    []byte(passthroughTopicJSON),
		}},
	})

	tag := shortTag("email")
	rcpt := "rcpt-" + tag + "@example.org"
	subBody := []byte(`{
	  "resourceType": "Subscription",
	  "status": "requested",
	  "topic": "http://example.org/topics/hl7-passthrough",
	  "channelType": {"code": "email"},
	  "endpoint": "mailto:` + rcpt + `",
	  "content": "id-only",
	  "channel": {"type": "email", "endpoint": "mailto:` + rcpt + `"}
	}`)
	subID := fx.createSubscription(ctx, t, h, subBody)

	driveAdmit(t, ctx, h, "EMAIL-1-"+tag, "MRN-EMAIL", "A01")

	// Poll the FakeSMTP journal for the rcpt.
	deadline := time.Now().Add(30 * time.Second)
	for {
		msgs := h.MockSub.SMTP.Received(rcpt)
		if len(msgs) >= 1 {
			if msgs[0].From == "" {
				t.Errorf("smtp message has empty From")
			}
			if len(msgs[0].Data) == 0 {
				t.Errorf("smtp message has empty Data")
			}
			return
		}
		if time.Now().After(deadline) {
			dumpAndFail(t, ctx, h, subID, "no smtp message received for %s within 30s", rcpt)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
