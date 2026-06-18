// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestE2E_ProdBinary_ProcessesHL7Message proves the production binary
// — not just the harness — runs the full pipeline end-to-end. A
// Subscription is registered (directly via SQL helpers, since the API
// surface is exercised by the sibling test), an HL7 v2 message is
// framed and sent over the binary's MLLP listener, and the rest-hook
// subscriber must receive the resulting notification within a few
// seconds.
//
// What this test pins that the harness Pipeline does NOT:
//
//   - cmd/fhir-subs's startup sequence builds a real
//     mllp.Listener bound to a TCP port,
//   - run.go's pipeline workers (hl7processor, matcher, submatcher,
//     scheduler) actually claim and forward the message,
//   - the MLLP -> hl7_message_queue -> resource_changes ->
//     ehr_events -> deliveries -> rest-hook chain works through the
//     production binary's wiring.
//
// B-4.
func TestE2E_ProdBinary_ProcessesHL7Message(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)

	// 1. Seed an active subscription pointing at the harness
	// mocksub rest-hook receiver.
	subID, err := RegisterSubscriber(ctx, h, RegisterSubscriberOptions{
		ClientID: "e2e-prod-hl7-client",
		TopicURL: "http://example.org/topic/observation",
		Endpoint: "http://" + h.MockSub.HTTPAddr + "/hook/e2e-prod-hl7-sub",
	})
	if err != nil {
		t.Fatalf("RegisterSubscriber: %v", err)
	}
	// RegisterSubscriber inserts requested status; flip to active so
	// the submatcher's "active subscriptions" filter sees it.
	if _, err := h.DB.Exec(ctx, `UPDATE subscriptions SET status='active' WHERE id=$1`, subID); err != nil {
		t.Fatalf("activate sub: %v", err)
	}

	// 2. Start the production binary with an MLLP listener.
	mllpPort := freePort(t)
	bin := startProdBinary(t, ctx, prodBinaryConfig{
		DatabaseURL: h.DBURL,
		FacilityID:  "e2e-prod-hl7",
		AdapterID:   "default",
		MLLPBind:    "127.0.0.1:" + mllpPort,
		Insecure:    true,
		GracePeriod: 5 * time.Second,
		// audience empty so the subscription can be created without
		// the bearer middleware in the way.
		AuthAudience: "",
	})
	defer bin.Stop(t, 10*time.Second)

	// 3. Send an HL7 v2 message via the binary's MLLP listener.
	sendHL7ToProdBinary(t, ctx, bin.MLLPAddr(), sampleHL7v2)

	// 4. Wait for the rest-hook receiver to record the notification.
	got, err := WaitForNotification(ctx, h, "e2e-prod-hl7-sub", 30*time.Second)
	if err != nil {
		// Dump some pipeline state to make a flake easier to debug.
		if pid, parseErr := uuid.Parse(subID); parseErr == nil {
			dumpPipelineState(t, ctx, h, pid)
		} else {
			t.Logf("could not parse subID for dump: %v", parseErr)
		}
		t.Fatalf("WaitForNotification: %v", err)
	}
	if got.SubscriptionID != "e2e-prod-hl7-sub" {
		t.Fatalf("subscription id: got %q", got.SubscriptionID)
	}
}

// sampleHL7v2 is a minimal ADT^A01 message used to drive a single
// HL7 v2 round-trip. The default adapter passes the bytes through as a
// resource_changes row of resource_type=Bundle, which the catalog has
// no specific topic mapping for — the test catalog is empty, so the
// matcher would never produce ehr_events. To keep the e2e test focused
// on the wiring (not on the catalog), we let the SUT use the default
// catalog provider; the assertion is "the rest-hook receives a
// notification at all", proving the chain is connected end-to-end.
//
// MLLP framing: 0x0B <body> 0x1C 0x0D
const sampleHL7v2 = "MSH|^~\\&|SENDING_APP|SENDING_FAC|RECEIVING_APP|RECEIVING_FAC|20260618120000||ADT^A01|MSG00001|P|2.5\r" +
	"EVN|A01|20260618120000\r" +
	"PID|1||PATID1234^5^M11^ADT1^MR^GOOD HEALTH HOSPITAL~123456789^^^USSSA^SS||EVERYWOMAN^EVE^E^^^^L|JONES|19620320|F||2106-3|153 FERNWOOD DR.^^STATESVILLE^OH^35292||(206)3345232|(206)752-121||M|NON|400003403~1129086\r" +
	"PV1|1|I|2000^2012^01||||004777^GOODDOC^GOODSON^J|||SUR|||||||004777^GOODDOC^GOODSON^J|S|VisitNumber^^^Adt^VN|A|||||||||||||||||||||||||199912271408\r"

func sendHL7ToProdBinary(t *testing.T, ctx context.Context, addr, body string) {
	t.Helper()
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Fatalf("dial mllp: %v", err)
	}
	defer func() { _ = conn.Close() }()

	frame := append([]byte{0x0B}, []byte(body)...)
	frame = append(frame, 0x1C, 0x0D)
	if err := conn.SetWriteDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set write deadline: %v", err)
	}
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write frame: %v", err)
	}

	// Try to read the ACK so the test waits for persistence-then-ACK
	// completion. Any read error here is non-fatal — the assertion
	// is the rest-hook receipt, not the wire-level ACK shape.
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, 1024)
	if n, _ := conn.Read(buf); n > 0 {
		t.Logf("mllp ack: %q", buf[:n])
	}
}

// (the package already provides dumpPipelineState in dump_state_test.go;
// this file calls it via the (sub uuid.UUID) signature.)
