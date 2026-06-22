// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Phase A (RED) integration test for OP #345 — Realstack simplification B.
//
// This test boots the consolidated cmd/test-receivers binary in-process
// (via runReceivers) and exercises EACH of the four receiver subsystems
// over real network sockets:
//
//   - rest-hook:8090/hook/{sub}       (HTTP POST + journal)
//   - websocket:8091/ws/subscriptions (WS subscriber + /events query API)
//   - mllp:8093 + :2575               (HTTP control plane + MLLP listener)
//   - smtp:1025 + query :1080         (SMTP MAIL FROM / RCPT TO / DATA)
//
// The harness uses ephemeral ports for each subsystem so the test does
// not collide with anything else on the host. The MLLP target is a
// real loopback MLLP listener bound by the test process; the receivers
// binary's MLLP control plane sends real frames to it.
//
// No mocks: every wire byte is real net/* traffic.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"sync"
	"testing"
	"time"
)

// receiversAddrs is the bag of host:port pairs the integration test
// hands runReceivers and then dials.
type receiversAddrs struct {
	resthookHTTP string
	wsQuery      string
	mllpHTTP     string
	smtpListen   string
	smtpQuery    string
	mllpTarget   string // loopback MLLP listener the test runs
}

func pickEphemeralPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	return l.Addr().String()
}

// startLoopbackMLLPForTest mirrors the local listener used by
// cmd/test-mllp-control-plane's unit tests. It's a real TCP server
// that speaks MLLP framing. NOT a Go-language fake of an external
// dependency — it is a real socket the binary connects to.
func startLoopbackMLLPForTest(t *testing.T) (addr string, frames *frameRecorder) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	rec := &frameRecorder{}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				var acc bytes.Buffer
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						acc.Write(buf[:n])
					}
					if bytes.Contains(acc.Bytes(), []byte{0x1C, 0x0D}) {
						break
					}
					if err != nil {
						return
					}
				}
				start := bytes.IndexByte(acc.Bytes(), 0x0B)
				end := bytes.LastIndex(acc.Bytes(), []byte{0x1C, 0x0D})
				if start < 0 || end <= start {
					return
				}
				body := acc.Bytes()[start+1 : end]
				rec.add(body)
				ack := buildLoopbackACK(body)
				_, _ = c.Write(ack)
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = l.Close() })
	return l.Addr().String(), rec
}

func buildLoopbackACK(body []byte) []byte {
	end := bytes.IndexByte(body, '\r')
	if end < 0 {
		end = len(body)
	}
	parts := strings.Split(string(body[:end]), "|")
	msgID := ""
	if len(parts) >= 10 {
		msgID = parts[9]
	}
	now := time.Now().UTC().Format("20060102150405")
	ack := "MSH|^~\\&|FHIRSUBS|TEST|MOCKEHR|E2E|" + now + "||ACK|ACK-" + msgID + "|T|2.5.1\rMSA|AA|" + msgID + "\r"
	out := []byte{0x0B}
	out = append(out, ack...)
	out = append(out, 0x1C, 0x0D)
	return out
}

type frameRecorder struct {
	mu     sync.Mutex
	frames [][]byte
}

func (f *frameRecorder) add(b []byte) {
	cp := make([]byte, len(b))
	copy(cp, b)
	f.mu.Lock()
	f.frames = append(f.frames, cp)
	f.mu.Unlock()
}

// snapshot is intentionally unexported and unused at the time of
// writing — the integration test asserts on the ACK echoed by the
// loopback listener through the receivers binary's HTTP response
// body. Tests that prefer to inspect the frames the binary emitted
// directly may add a getter; until then, the recorder still keeps
// the captured bytes alive for inspection during a debugger session.
var _ = (*frameRecorder)(nil).add

// startReceiversForTest brings up the consolidated binary (in-process
// for unit-level integration) on ephemeral ports. The function returns
// the addresses + a stop closure; it blocks until each subsystem's
// healthz reports ready.
func startReceiversForTest(t *testing.T) (receiversAddrs, func()) {
	t.Helper()

	addrs := receiversAddrs{
		resthookHTTP: pickEphemeralPort(t),
		wsQuery:      pickEphemeralPort(t),
		mllpHTTP:     pickEphemeralPort(t),
		smtpListen:   pickEphemeralPort(t),
		smtpQuery:    pickEphemeralPort(t),
	}
	addrs.mllpTarget, _ = startLoopbackMLLPForTest(t)

	cfg := receiversConfig{
		RestHookAddr: addrs.resthookHTTP,
		WSQueryAddr:  addrs.wsQuery,
		MLLPHTTPAddr: addrs.mllpHTTP,
		MLLPTarget:   addrs.mllpTarget,
		SMTPListen:   addrs.smtpListen,
		SMTPQuery:    addrs.smtpQuery,
	}
	rs, err := newReceivers(cfg)
	if err != nil {
		t.Fatalf("newReceivers: %v", err)
	}
	if err := rs.Start(); err != nil {
		t.Fatalf("start receivers: %v", err)
	}

	// Wait for each subsystem's /healthz to report ready.
	endpoints := []string{
		"http://" + addrs.resthookHTTP + "/healthz",
		"http://" + addrs.wsQuery + "/healthz",
		"http://" + addrs.mllpHTTP + "/healthz",
		"http://" + addrs.smtpQuery + "/healthz",
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, u := range endpoints {
		for {
			resp, err := http.Get(u)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					break
				}
			}
			if time.Now().After(deadline) {
				rs.Close()
				t.Fatalf("%s never reported ready", u)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	return addrs, func() { rs.Close() }
}

// TestConsolidated_RestHookPath exercises the rest-hook receiver in
// the consolidated binary: real POST /hook/{sub}, captured journal,
// /notifications query.
func TestConsolidated_RestHookPath(t *testing.T) {
	addrs, stop := startReceiversForTest(t)
	defer stop()

	resp, err := http.Post(
		"http://"+addrs.resthookHTTP+"/hook/sub-1",
		"application/fhir+json",
		strings.NewReader(`{"resourceType":"Bundle"}`),
	)
	if err != nil {
		t.Fatalf("POST /hook: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status %d", resp.StatusCode)
	}

	q, err := http.Get("http://" + addrs.resthookHTTP + "/notifications/sub-1")
	if err != nil {
		t.Fatalf("GET /notifications/sub-1: %v", err)
	}
	defer q.Body.Close()
	var got []ReceivedRequest
	if err := json.NewDecoder(q.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	if got[0].SubscriptionID != "sub-1" {
		t.Errorf("sub id %q", got[0].SubscriptionID)
	}
}

// TestConsolidated_WSPath exercises the websocket receiver. Without a
// peer to bind, the test asserts the query API is up + healthz responds
// — bind/connect is exercised by the full realstack docker test.
func TestConsolidated_WSPath(t *testing.T) {
	addrs, stop := startReceiversForTest(t)
	defer stop()

	resp, err := http.Get("http://" + addrs.wsQuery + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	// Empty journal renders as `null` or `[]` per encoder defaults; both are valid.
	if !(strings.HasPrefix(string(body), "[") || strings.HasPrefix(string(body), "null")) {
		t.Errorf("unexpected /events body: %q", body)
	}
}

// TestConsolidated_MLLPPath exercises the MLLP control plane: the
// HTTP endpoint synthesizes an HL7 frame and emits it over real TCP
// to the loopback MLLP listener the test bound.
func TestConsolidated_MLLPPath(t *testing.T) {
	// Capture frames via a closure linked to the loopback listener.
	addrs, stop := startReceiversForTest(t)
	defer stop()

	// Re-derive the recorder from the test-bound listener. The
	// loopback listener is held inside the harness; we already
	// retrieved its addr in addrs.mllpTarget. Re-query frames via a
	// second loopback listener would create a second listener — so
	// instead we use this test's startLoopbackMLLPForTest output.
	// startReceiversForTest spun one up; we test by sending a
	// scenario and asserting the HTTP response carries an ACK.
	body, _ := json.Marshal(map[string]string{
		"patient_id":  "MRN-1",
		"message_id":  "ADT-1",
		"trigger":     "A01",
		"family_name": "Smith",
		"given_name":  "Pat",
	})
	resp, err := http.Post(
		"http://"+addrs.mllpHTTP+"/scenarios/admit_patient",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("POST /scenarios/admit_patient: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	var ack struct {
		ACK string `json:"ack"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ack); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if !strings.Contains(ack.ACK, "MSA|AA|ADT-1") {
		t.Errorf("ack missing MSA|AA|ADT-1: %q", ack.ACK)
	}
}

// TestConsolidated_SMTPPath exercises the SMTP receiver. Real
// net/smtp client → real go-smtp listener → /messages query API.
func TestConsolidated_SMTPPath(t *testing.T) {
	addrs, stop := startReceiversForTest(t)
	defer stop()

	body := "From: a@x\r\nTo: b@x\r\nSubject: integ\r\n\r\nhello\r\n"
	if err := smtp.SendMail(addrs.smtpListen, nil, "a@x", []string{"b@x"}, []byte(body)); err != nil {
		t.Fatalf("SendMail: %v", err)
	}

	q, err := http.Get("http://" + addrs.smtpQuery + "/messages")
	if err != nil {
		t.Fatalf("GET /messages: %v", err)
	}
	defer q.Body.Close()
	var msgs []capturedSMTPMessage
	if err := json.NewDecoder(q.Body).Decode(&msgs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d, want 1", len(msgs))
	}
	if !strings.Contains(msgs[0].Data, "Subject: integ") {
		t.Errorf("data missing subject: %q", msgs[0].Data)
	}
}

// TestConsolidated_AllFourSubsystemsConcurrent is the smoke test that
// pins ALL four subsystems are up and serving in the same process at
// the same time. If a future refactor accidentally serializes startup
// or steals a port, this test fails.
func TestConsolidated_AllFourSubsystemsConcurrent(t *testing.T) {
	addrs, stop := startReceiversForTest(t)
	defer stop()

	checks := []struct {
		name string
		url  string
	}{
		{"resthook", "http://" + addrs.resthookHTTP + "/healthz"},
		{"ws", "http://" + addrs.wsQuery + "/healthz"},
		{"mllp", "http://" + addrs.mllpHTTP + "/healthz"},
		{"smtp", "http://" + addrs.smtpQuery + "/healthz"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, len(checks))
	for _, c := range checks {
		c := c
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				errs <- err
				return
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent healthz: %v", err)
		}
	}
}
