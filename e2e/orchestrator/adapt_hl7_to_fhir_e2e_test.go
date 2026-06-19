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

// TestScenario_AdaptHL7ToFHIR covers the HL7 Message Processor's
// translate-and-emit path against the production binary's MLLP listener
// running in the realstack. A real ORM message is framed and sent to
// the binary; the adapter emits a ServiceRequest resource_change; the
// rest-hook subscriber receives a delivery whose body is FHIR JSON.
//
// Replaces the t.Skip stub previously in skipped_scenarios_test.go for
// OpenProject story #145.
func TestScenario_AdaptHL7ToFHIR(t *testing.T) {
	s := bootForScenario(t, realstack.Options{EnableMLLP: true})
	tag := shortTagFor(t)

	subID := s.postSubscription(restHookSubscriptionJSON(s.stack,
		"http://example.org/topics/hl7-passthrough", tag))
	_ = subID

	if s.stack.Binary.MLLPAddr == "" {
		t.Fatal("realstack binary has no MLLP listener; AdaptHL7ToFHIR cannot run without MLLP")
	}

	// Send a minimal ORM frame to the prod binary's MLLP listener.
	// MLLP framing: <SB>data<EB><CR>.
	frame := []byte("\x0bMSH|^~\\&|EHR|FAC|FHIRSUBS|FAC|20260619||ORM^O01|MSG-AdaptHL7-1|P|2.5\r" +
		"PID|1||MRN-AdaptHL7^^^FAC^MR\r" +
		"ORC|NW|PO-1|FO-1\r" +
		"OBR|1|PO-1|FO-1|TEST^Genetic Panel\r\x1c\r")

	conn, err := net.DialTimeout("tcp", s.stack.Binary.MLLPAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial MLLP %s: %v", s.stack.Binary.MLLPAddr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write MLLP frame: %v", err)
	}
	// Read ACK so we know the binary persisted; ignore body.
	buf := make([]byte, 4096)
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("read MLLP ACK: %v", err)
	}

	got := s.waitForRestHookNotifications(tag, 1, 90*time.Second)
	if len(got) == 0 {
		t.Fatalf("AdaptHL7ToFHIR: rest-hook subscriber received 0 notifications after ORM message; HL7 -> FHIR translation not wired")
	}
	for i, n := range got {
		if !contains(n.Body, "ServiceRequest") {
			t.Errorf("delivery %d body does not look like FHIR ServiceRequest: %s", i, n.Body)
		}
	}
}

// contains is a tiny strings-helper so this file imports nothing it
// would not otherwise need; the orchestrator package already has utility
// helpers but they live behind the `e2e` build tag.
func contains(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
