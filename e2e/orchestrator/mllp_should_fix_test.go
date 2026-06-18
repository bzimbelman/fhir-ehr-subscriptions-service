// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/mllp"
)

// TestE2E_S9_5_ExtractMSHCharset — MSH-18 (charset) is now surfaced
// from ExtractMSH so callers can metric the encoding.
func TestE2E_S9_5_ExtractMSHCharset(t *testing.T) {
	body := []byte("MSH|^~\\&|SENDER|FAC|RECV|RECF|20260101120000||ORU^R01|MID|P|2.5.1||||||UNICODE UTF-8\r")
	got, err := mllp.ExtractMSH(body)
	if err != nil {
		t.Fatalf("ExtractMSH: %v", err)
	}
	if got.Charset != "UNICODE UTF-8" {
		t.Errorf("Charset got %q want %q", got.Charset, "UNICODE UTF-8")
	}
	if got.MessageType != "ORU" {
		t.Errorf("MessageType got %q want ORU", got.MessageType)
	}
}

// TestE2E_S9_10_ExtractMSHTimestamp — MSH-7 (sender timestamp) is now
// surfaced so the HL7 processor can source `occurred` from the EHR's
// stamped time rather than wall-clock now().
func TestE2E_S9_10_ExtractMSHTimestamp(t *testing.T) {
	body := []byte("MSH|^~\\&|SENDER|FAC|RECV|RECF|20260101120000||ORU^R01|MID|P|2.5.1\r")
	got, err := mllp.ExtractMSH(body)
	if err != nil {
		t.Fatalf("ExtractMSH: %v", err)
	}
	if got.MessageDateTime != "20260101120000" {
		t.Errorf("MessageDateTime got %q want %q", got.MessageDateTime, "20260101120000")
	}
}

// TestE2E_S9_4_FramerPendingBound — pending growth past 2x maxBody
// surfaces Malformed{Oversized} so the connection is dropped (S-9.4).
func TestE2E_S9_4_FramerPendingBound(t *testing.T) {
	const maxBody = 256
	f := mllp.NewFramer(maxBody)
	noise := strings.Repeat("X", 4*maxBody)
	f.Append([]byte(noise))
	ev := f.Next()
	mal, ok := ev.(mllp.MalformedEvent)
	if !ok {
		t.Fatalf("expected MalformedEvent, got %#v", ev)
	}
	if mal.Reason != mllp.ReasonOversizedMessage {
		t.Errorf("reason got %q want OversizedMessage", mal.Reason)
	}
}
