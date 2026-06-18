// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package mockehr

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The scenario control plane is the orchestrator's primary affordance.
// Each scenario name is a POST endpoint that takes a small JSON config
// and emits a matching HL7 message into a configured MLLP target.
//
// The control plane runs alongside the FHIR mock on the same HTTP
// handler. For tests, we stand up an MLLP echo listener and configure
// the control plane to dial it; we then assert the listener received
// what the scenario was supposed to emit.

func TestControlPlane_AdmitPatient_EmitsADT(t *testing.T) {
	t.Parallel()
	got := startEchoMLLPListenerAccumulating(t)
	defer got.Close()

	cp := NewControlPlane(ControlPlaneConfig{MLLPTarget: got.Addr().String()})
	srv := httptest.NewServer(cp.Handler())
	defer srv.Close()

	body := map[string]any{
		"patient_id":  "MRN9001",
		"message_id":  "ADT-CP-1",
		"trigger":     "A01",
		"family_name": "Doe",
		"given_name":  "Jane",
	}
	postJSON(t, srv.URL+"/scenarios/admit_patient", body)

	frame := got.WaitForFrame(t, 2*time.Second)
	if !bytes.Contains(frame, []byte("|ADT^A01|")) {
		t.Fatalf("frame did not contain ADT^A01: %s", frame)
	}
	if !bytes.Contains(frame, []byte("|ADT-CP-1|")) {
		t.Fatalf("frame did not contain control id ADT-CP-1: %s", frame)
	}
}

func TestControlPlane_PlaceOrder_EmitsORM(t *testing.T) {
	t.Parallel()
	got := startEchoMLLPListenerAccumulating(t)
	defer got.Close()

	cp := NewControlPlane(ControlPlaneConfig{MLLPTarget: got.Addr().String()})
	srv := httptest.NewServer(cp.Handler())
	defer srv.Close()

	body := map[string]any{
		"placer_order_id":  "PL-CP-1",
		"filler_order_id":  "FL-CP-1",
		"patient_id":       "MRN9001",
		"message_id":       "ORM-CP-1",
		"universal_svc_id": "GLU^Glucose^L",
	}
	postJSON(t, srv.URL+"/scenarios/place_order", body)

	frame := got.WaitForFrame(t, 2*time.Second)
	if !bytes.Contains(frame, []byte("|ORM^O01|")) {
		t.Fatalf("frame did not contain ORM^O01: %s", frame)
	}
	if !bytes.Contains(frame, []byte("ORC|NW|")) {
		t.Fatalf("frame did not contain ORC|NW|: %s", frame)
	}
}

func TestControlPlane_CancelAndReplaceOrder_EmitsTwoLinkedFrames(t *testing.T) {
	t.Parallel()
	got := startEchoMLLPListenerAccumulating(t)
	defer got.Close()

	cp := NewControlPlane(ControlPlaneConfig{MLLPTarget: got.Addr().String()})
	srv := httptest.NewServer(cp.Handler())
	defer srv.Close()

	body := map[string]any{
		"placer_order_id":        "PL-CP-2",
		"filler_order_id":        "FL-CP-2",
		"patient_id":             "MRN9002",
		"cancel_message_id":      "ORM-CA-CP-2",
		"replacement_message_id": "ORM-NW-CP-2",
	}
	postJSON(t, srv.URL+"/scenarios/cancel_and_replace_order", body)

	cancelFrame := got.WaitForFrame(t, 2*time.Second)
	replaceFrame := got.WaitForFrame(t, 2*time.Second)

	if !bytes.Contains(cancelFrame, []byte("ORC|CA|PL-CP-2|FL-CP-2")) {
		t.Fatalf("cancel frame missing ORC|CA|PL-CP-2|FL-CP-2: %s", cancelFrame)
	}
	if !bytes.Contains(replaceFrame, []byte("ORC|NW|PL-CP-2|FL-CP-2")) {
		t.Fatalf("replace frame missing ORC|NW|PL-CP-2|FL-CP-2: %s", replaceFrame)
	}
}

func TestControlPlane_BurstMessages_EmitsN(t *testing.T) {
	t.Parallel()
	got := startEchoMLLPListenerAccumulating(t)
	defer got.Close()

	cp := NewControlPlane(ControlPlaneConfig{MLLPTarget: got.Addr().String()})
	srv := httptest.NewServer(cp.Handler())
	defer srv.Close()

	body := map[string]any{
		"count":      5,
		"trigger":    "A04",
		"patient_id": "MRN-BURST",
	}
	postJSON(t, srv.URL+"/scenarios/burst_messages", body)

	for i := 0; i < 5; i++ {
		frame := got.WaitForFrame(t, 3*time.Second)
		if !bytes.Contains(frame, []byte("|ADT^A04|")) {
			t.Fatalf("burst frame %d missing ADT^A04: %s", i, frame)
		}
	}
}

// --- Local helpers (test-only) ----------------------------------------

type accumulatingListener struct {
	*echoListener
	frames chan []byte
}

func (a *accumulatingListener) WaitForFrame(t *testing.T, d time.Duration) []byte {
	t.Helper()
	select {
	case f := <-a.frames:
		return f
	case <-time.After(d):
		t.Fatalf("WaitForFrame timed out after %s", d)
		return nil
	}
}

func startEchoMLLPListenerAccumulating(t *testing.T) *accumulatingListener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	frames := make(chan []byte, 16)
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c io.ReadWriteCloser) {
				defer c.Close()
				buf := make([]byte, 4096)
				var acc bytes.Buffer
				deadline := time.Now().Add(2 * time.Second)
				for {
					if d, ok := c.(interface{ SetReadDeadline(time.Time) error }); ok {
						_ = d.SetReadDeadline(deadline)
					}
					n, err := c.Read(buf)
					if n > 0 {
						acc.Write(buf[:n])
						// Emit complete frames as we see them.
						for {
							s := bytes.IndexByte(acc.Bytes(), 0x0B)
							e := bytes.Index(acc.Bytes(), []byte{0x1C, 0x0D})
							if s < 0 || e < 0 || e <= s {
								break
							}
							frame := append([]byte(nil), acc.Bytes()[s:e+2]...)
							body, _ := unframeMLLP(frame)
							frames <- body
							ack := buildACK(ackKindApplicationAccept, extractMSH10(body))
							_, _ = c.Write(frameMLLP([]byte(ack)))
							// Drop the consumed bytes.
							rest := append([]byte(nil), acc.Bytes()[e+2:]...)
							acc.Reset()
							acc.Write(rest)
						}
					}
					if err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return &accumulatingListener{
		echoListener: &echoListener{l: l},
		frames:       frames,
	}
}

func postJSON(t *testing.T, url string, body any) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("post %s: status %d body=%s", url, resp.StatusCode, got)
	}
}

func TestControlPlane_BadInputReturns400(t *testing.T) {
	t.Parallel()
	cp := NewControlPlane(ControlPlaneConfig{MLLPTarget: "127.0.0.1:1"})
	srv := httptest.NewServer(cp.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/scenarios/admit_patient",
		"application/json",
		strings.NewReader(`{not json}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", resp.StatusCode)
	}
}

// Compile-time check that the scenario list matches the LLD's required
// endpoints.
func TestControlPlane_AllScenariosRouted(t *testing.T) {
	t.Parallel()
	cp := NewControlPlane(ControlPlaneConfig{MLLPTarget: "127.0.0.1:1"})
	got := cp.RegisteredScenarios()
	want := []string{
		"admit_patient",
		"place_order",
		"finalize_lab",
		"cancel_and_replace_order",
		"burst_messages",
	}
	if len(got) != len(want) {
		t.Fatalf("scenarios: got %v want %v", got, want)
	}
	gotSet := map[string]bool{}
	for _, s := range got {
		gotSet[s] = true
	}
	for _, s := range want {
		if !gotSet[s] {
			t.Errorf("missing scenario %q", s)
		}
	}
	_ = context.Background()
}
