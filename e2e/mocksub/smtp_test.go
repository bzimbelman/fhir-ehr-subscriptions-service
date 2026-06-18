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
	s, startErr := StartFakeSMTP("127.0.0.1:0")
	if startErr != nil {
		t.Fatalf("start smtp: %v", startErr)
	}
	defer s.Close()

	addr := s.Addr().String()
	host, _, _ := net.SplitHostPort(addr)
	c, dialErr := smtp.Dial(addr)
	if dialErr != nil {
		t.Fatalf("smtp.Dial: %v", dialErr)
	}
	defer c.Close()

	if helloErr := c.Hello(host); helloErr != nil {
		t.Fatalf("hello: %v", helloErr)
	}
	if mailErr := c.Mail("sender@example.com"); mailErr != nil {
		t.Fatalf("mail: %v", mailErr)
	}
	if rcptErr := c.Rcpt("subscriber@example.com"); rcptErr != nil {
		t.Fatalf("rcpt: %v", rcptErr)
	}
	wc, dataErr := c.Data()
	if dataErr != nil {
		t.Fatalf("data: %v", dataErr)
	}
	body := "Subject: Notification\r\n\r\nHello world\r\n"
	if _, writeErr := wc.Write([]byte(body)); writeErr != nil {
		t.Fatalf("write: %v", writeErr)
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
