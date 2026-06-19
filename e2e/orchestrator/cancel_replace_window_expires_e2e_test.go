// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

package orchestrator

import (
	"net"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/realstack"
)

// TestScenario_CancelAndReplaceWindowExpires covers the pending_pairs
// reaper. When an HL7 cancel arrives but no replacement follows within
// the configured correlation window, the reaper SHOULD release the
// orphaned cancel as its own resource_change so downstream subscribers
// see the cancellation rather than waiting forever for a paired NW.
//
// Replaces the t.Skip stub previously in skipped_scenarios_test.go for
// OpenProject story #145.
func TestScenario_CancelAndReplaceWindowExpires(t *testing.T) {
	s := bootForScenario(t, realstack.Options{EnableMLLP: true})
	tag := shortTagFor(t)

	subID := s.postSubscription(restHookSubscriptionJSON(s.stack,
		"http://example.org/topics/hl7-passthrough", tag))
	_ = subID

	if s.stack.Binary.MLLPAddr == "" {
		t.Fatal("realstack binary has no MLLP listener; window-expires scenario cannot run without MLLP")
	}

	// Send only the cancel; never send the replacement.
	frame := []byte("\x0bMSH|^~\\&|EHR|FAC|FHIRSUBS|FAC|20260619||ORM^O01|MSG-WinExpires-1|P|2.5\r" +
		"PID|1||MRN-WinExpires^^^FAC^MR\r" +
		"ORC|CA|PO-WIN-1|FO-WIN-1\r" +
		"OBR|1|PO-WIN-1|FO-WIN-1|TEST^Genetic Panel\r\x1c\r")

	conn, err := net.DialTimeout("tcp", s.stack.Binary.MLLPAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial MLLP: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write MLLP frame: %v", err)
	}
	buf := make([]byte, 4096)
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("read MLLP ACK: %v", err)
	}
	_ = conn.Close()

	// The reaper window in tests is short (configured via the harness's
	// rendered config) but a real reaper run still takes a few seconds.
	got := s.waitForRestHookNotifications(tag, 1, 120*time.Second)
	if len(got) == 0 {
		t.Fatalf("CancelAndReplaceWindowExpires: reaper did not release orphaned cancel after window; got 0 deliveries")
	}
}
