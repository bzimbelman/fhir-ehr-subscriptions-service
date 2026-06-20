// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"errors"
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
// surfaces oversized so the connection is dropped (S-9.4).
//
// Post-OP #227, Framer.Append rejects an oversized append eagerly with
// ErrPendingExceeded; the connection loop treats that error identically
// to a MalformedEvent{ReasonOversizedMessage} (both cause the peer to
// be dropped). We accept either signal, matching the internal
// counterpart in internal/mllp/should_fix_test.go.
func TestE2E_S9_4_FramerPendingBound(t *testing.T) {
	const maxBody = 256
	f := mllp.NewFramer(maxBody)
	noise := strings.Repeat("X", 4*maxBody)
	if err := f.Append([]byte(noise)); err != nil {
		// Eager rejection path — accepted; both signals drop the peer.
		if !errors.Is(err, mllp.ErrPendingExceeded) {
			t.Fatalf("Append err=%v, want errors.Is(err, ErrPendingExceeded)=true", err)
		}
		return
	}
	// Legacy fallback: framer accepted but Next() must surface oversized.
	ev := f.Next()
	mal, ok := ev.(mllp.MalformedEvent)
	if !ok {
		t.Fatalf("expected MalformedEvent, got %#v", ev)
	}
	if mal.Reason != mllp.ReasonOversizedMessage {
		t.Errorf("reason got %q want OversizedMessage", mal.Reason)
	}
}
