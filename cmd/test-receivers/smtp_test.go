// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Phase A (RED) tests for OP #345 — Realstack simplification B.
//
// These unit tests pin the public contract of the SMTP receiver
// subsystem inside cmd/test-receivers. The receiver replaces the
// mailpit container with a real github.com/emersion/go-smtp server +
// a /messages query API the email-channel tests assert against.
//
// The tests deliver real SMTP traffic on the wire (net/smtp client →
// real TCP socket → real go-smtp listener) and assert the captured
// state surfaces through the query API. No mocks of SMTP anywhere.
package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"testing"
	"time"
)

// startSMTPReceiverForTest binds the SMTP listener and the query API
// on ephemeral ports and returns their addresses + a stop closure. The
// SMTP server is the production receiver type — no Go-language fake.
func startSMTPReceiverForTest(t *testing.T) (smtpAddr, queryURL string, stop func()) {
	t.Helper()
	// Pick two ephemeral ports the receiver binds to. We listen and
	// close so the kernel hands us a free port; the small window
	// between Close() and the receiver's own Listen() is acceptable
	// for a unit test.
	smtpL, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen smtp: %v", err)
	}
	smtpAddr = smtpL.Addr().String()
	_ = smtpL.Close()

	qL, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen query: %v", err)
	}
	queryAddr := qL.Addr().String()
	_ = qL.Close()

	rcv, err := newSMTPReceiver(smtpAddr, queryAddr)
	if err != nil {
		t.Fatalf("new smtp receiver: %v", err)
	}
	if err := rcv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Poll the query API's /healthz until ready (or fail in 2s).
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := http.Get("http://" + queryAddr + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			rcv.Close()
			t.Fatalf("smtp query API never reported ready")
		}
		time.Sleep(20 * time.Millisecond)
	}

	return smtpAddr, "http://" + queryAddr, func() { rcv.Close() }
}

// sendOneMail issues a real SMTP DATA command via net/smtp against the
// bound listener and returns once the server has acknowledged it.
func sendOneMail(t *testing.T, smtpAddr, from string, to []string, msg string) {
	t.Helper()
	if err := smtp.SendMail(smtpAddr, nil, from, to, []byte(msg)); err != nil {
		t.Fatalf("smtp.SendMail: %v", err)
	}
}

// fetchMessages returns the JSON-decoded message list from the query
// API. The query API contract is defined by this test.
func fetchMessages(t *testing.T, queryURL string) []capturedSMTPMessage {
	t.Helper()
	resp, err := http.Get(queryURL + "/messages")
	if err != nil {
		t.Fatalf("GET /messages: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("/messages: status %d: %s", resp.StatusCode, body)
	}
	var out []capturedSMTPMessage
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// TestSMTPReceiver_CapturesMail asserts a real SMTP DATA flow lands in
// the captured journal and surfaces through GET /messages with the
// right envelope + headers + body.
func TestSMTPReceiver_CapturesMail(t *testing.T) {
	smtpAddr, queryURL, stop := startSMTPReceiverForTest(t)
	defer stop()

	body := "From: alice@example.com\r\n" +
		"To: bob@example.com\r\n" +
		"Subject: hello\r\n" +
		"X-Test: t1\r\n" +
		"\r\n" +
		"hi bob\r\n"
	sendOneMail(t, smtpAddr, "alice@example.com", []string{"bob@example.com"}, body)

	msgs := fetchMessages(t, queryURL)
	if len(msgs) != 1 {
		t.Fatalf("captured %d, want 1", len(msgs))
	}
	m := msgs[0]
	if m.From != "alice@example.com" {
		t.Errorf("From=%q want alice@example.com", m.From)
	}
	if len(m.Recipients) != 1 || m.Recipients[0] != "bob@example.com" {
		t.Errorf("Recipients=%v want [bob@example.com]", m.Recipients)
	}
	if !strings.Contains(m.Data, "Subject: hello") {
		t.Errorf("Data missing Subject: hello: %q", m.Data)
	}
	// Header parsing is part of the contract — the test asserts the
	// receiver normalized headers into Headers map for asserts.
	if got := m.Headers["X-Test"]; got != "t1" {
		t.Errorf("Headers[X-Test]=%q want t1", got)
	}
	if got := m.Headers["Subject"]; got != "hello" {
		t.Errorf("Headers[Subject]=%q want hello", got)
	}
}

// TestSMTPReceiver_MultipleRecipients pins multi-RCPT envelopes.
func TestSMTPReceiver_MultipleRecipients(t *testing.T) {
	smtpAddr, queryURL, stop := startSMTPReceiverForTest(t)
	defer stop()

	body := "From: a@x\r\nTo: b@x, c@x\r\nSubject: multi\r\n\r\n.\r\n"
	sendOneMail(t, smtpAddr, "a@x", []string{"b@x", "c@x"}, body)

	msgs := fetchMessages(t, queryURL)
	if len(msgs) != 1 {
		t.Fatalf("got %d, want 1", len(msgs))
	}
	if len(msgs[0].Recipients) != 2 {
		t.Fatalf("recipients %v, want 2", msgs[0].Recipients)
	}
}

// TestSMTPReceiver_QueryByRecipient pins GET /messages?to=<addr>
// filtering. Email channel tests rely on per-recipient assertions
// without scanning every captured message.
func TestSMTPReceiver_QueryByRecipient(t *testing.T) {
	smtpAddr, queryURL, stop := startSMTPReceiverForTest(t)
	defer stop()

	sendOneMail(t, smtpAddr, "a@x", []string{"alice@x"}, "From: a@x\r\nTo: alice@x\r\nSubject: 1\r\n\r\nhi\r\n")
	sendOneMail(t, smtpAddr, "a@x", []string{"bob@x"}, "From: a@x\r\nTo: bob@x\r\nSubject: 2\r\n\r\nhi\r\n")

	resp, err := http.Get(queryURL + "/messages?to=bob@x")
	if err != nil {
		t.Fatalf("GET ?to=: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var out []capturedSMTPMessage
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d, want 1 (filter by recipient)", len(out))
	}
	if out[0].Recipients[0] != "bob@x" {
		t.Errorf("filter returned wrong msg: %v", out[0].Recipients)
	}
}

// TestSMTPReceiver_Reset clears the journal so subsequent tests start
// from a clean slate without restarting the binary.
func TestSMTPReceiver_Reset(t *testing.T) {
	smtpAddr, queryURL, stop := startSMTPReceiverForTest(t)
	defer stop()

	sendOneMail(t, smtpAddr, "a@x", []string{"b@x"}, "From: a@x\r\nTo: b@x\r\n\r\nx\r\n")
	if msgs := fetchMessages(t, queryURL); len(msgs) != 1 {
		t.Fatalf("expected one captured: %d", len(msgs))
	}

	resp, err := http.Post(queryURL+"/reset", "", nil)
	if err != nil {
		t.Fatalf("POST /reset: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("/reset status %d", resp.StatusCode)
	}

	if msgs := fetchMessages(t, queryURL); len(msgs) != 0 {
		t.Fatalf("expected 0 after reset, got %d", len(msgs))
	}
}

// TestSMTPReceiver_HealthzEvenWithNoMail asserts /healthz reports
// ready before any traffic — the docker-compose healthcheck depends on
// it.
func TestSMTPReceiver_HealthzEvenWithNoMail(t *testing.T) {
	_, queryURL, stop := startSMTPReceiverForTest(t)
	defer stop()

	resp, err := http.Get(queryURL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz status %d", resp.StatusCode)
	}
}

// TestSMTPReceiver_RejectsTooLargeMessage pins the MaxMessageBytes
// safeguard. The receiver must hold a generous-but-bounded ceiling so
// a runaway producer can't OOM the test container.
func TestSMTPReceiver_RejectsTooLargeMessage(t *testing.T) {
	smtpAddr, queryURL, stop := startSMTPReceiverForTest(t)
	defer stop()

	// 6 MiB body — receiver's MaxMessageBytes is 5 MiB.
	huge := strings.Repeat("X", 6*1024*1024)
	body := "From: a@x\r\nTo: b@x\r\nSubject: huge\r\n\r\n" + huge + "\r\n"

	err := smtp.SendMail(smtpAddr, nil, "a@x", []string{"b@x"}, []byte(body))
	if err == nil {
		t.Fatalf("smtp.SendMail accepted oversized message; want error")
	}

	// Nothing should be journaled — partial accept would be a leak.
	if msgs := fetchMessages(t, queryURL); len(msgs) != 0 {
		t.Errorf("oversized message journaled %d entries; want 0", len(msgs))
	}
}
