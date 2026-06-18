// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package mocksub

import (
	"net"
	"net/smtp"
	"strings"
	"testing"
	"time"
)

// FakeSMTP is the subscriber-side fake mail server. It accepts inbound
// SMTP messages, journals them keyed by recipient (RCPT TO), and exposes
// a control plane that mirrors the rest-hook journal shape so the
// orchestrator can use the same WaitForNotification helper.

func TestFakeSMTP_AcceptsAndJournalsMessage(t *testing.T) {
	t.Parallel()
	s, err := StartFakeSMTP("127.0.0.1:0")
	if err != nil {
		t.Fatalf("start smtp: %v", err)
	}
	defer s.Close()

	addr := s.Addr().String()
	host, _, _ := net.SplitHostPort(addr)
	c, err := smtp.Dial(addr)
	if err != nil {
		t.Fatalf("smtp.Dial: %v", err)
	}
	defer c.Close()

	if err := c.Hello(host); err != nil {
		t.Fatalf("hello: %v", err)
	}
	if err := c.Mail("sender@example.com"); err != nil {
		t.Fatalf("mail: %v", err)
	}
	if err := c.Rcpt("subscriber@example.com"); err != nil {
		t.Fatalf("rcpt: %v", err)
	}
	wc, err := c.Data()
	if err != nil {
		t.Fatalf("data: %v", err)
	}
	body := "Subject: Notification\r\n\r\nHello world\r\n"
	if _, err := wc.Write([]byte(body)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := wc.Close(); err != nil {
		t.Fatalf("close data: %v", err)
	}
	if err := c.Quit(); err != nil {
		t.Fatalf("quit: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := s.Received("subscriber@example.com"); len(got) >= 1 {
			if !strings.Contains(string(got[0].Data), "Hello world") {
				t.Fatalf("body mismatch: %q", got[0].Data)
			}
			if got[0].From != "sender@example.com" {
				t.Fatalf("from: got %q want sender@example.com", got[0].From)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("did not journal SMTP message within deadline")
}
