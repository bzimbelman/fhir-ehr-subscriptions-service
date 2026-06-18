// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package mocksub

import (
	"io"
	"net"
	"sync"
	"time"

	"github.com/emersion/go-smtp"
)

// SMTPMessage is one journaled inbound SMTP message.
type SMTPMessage struct {
	From       string
	Recipients []string
	Data       []byte
	ReceivedAt time.Time
}

// FakeSMTP is the subscriber-side fake SMTP server. It accepts plain
// SMTP (no TLS, no AUTH) and journals each accepted message.
type FakeSMTP struct {
	server   *smtp.Server
	listener net.Listener

	mu      sync.Mutex
	journal []SMTPMessage
}

// StartFakeSMTP binds the server on addr and starts serving in a
// background goroutine. addr can be "127.0.0.1:0" for an ephemeral bind.
func StartFakeSMTP(addr string) (*FakeSMTP, error) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	f := &FakeSMTP{listener: l}
	be := &fakeSMTPBackend{f: f}
	s := smtp.NewServer(be)
	s.Addr = l.Addr().String()
	s.Domain = "mocksub.local"
	s.AllowInsecureAuth = true
	s.ReadTimeout = 10 * time.Second
	s.WriteTimeout = 10 * time.Second
	s.MaxMessageBytes = 5 * 1024 * 1024
	s.MaxRecipients = 50
	f.server = s
	go func() {
		_ = s.Serve(l)
	}()
	return f, nil
}

// Addr returns the bound network address.
func (f *FakeSMTP) Addr() net.Addr {
	return f.listener.Addr()
}

// Close stops serving and frees the listener.
func (f *FakeSMTP) Close() error {
	_ = f.server.Close()
	return f.listener.Close()
}

// Received returns the messages journaled for a recipient, or all
// messages if rcpt == "".
func (f *FakeSMTP) Received(rcpt string) []SMTPMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]SMTPMessage, 0, len(f.journal))
	for _, m := range f.journal {
		if rcpt == "" {
			out = append(out, m)
			continue
		}
		for _, r := range m.Recipients {
			if r == rcpt {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

func (f *FakeSMTP) record(msg SMTPMessage) {
	msg.ReceivedAt = time.Now().UTC()
	f.mu.Lock()
	f.journal = append(f.journal, msg)
	f.mu.Unlock()
}

// --- go-smtp Backend / Session glue --------------------------------------

type fakeSMTPBackend struct {
	f *FakeSMTP
}

func (b *fakeSMTPBackend) NewSession(_ *smtp.Conn) (smtp.Session, error) {
	return &fakeSMTPSession{f: b.f}, nil
}

type fakeSMTPSession struct {
	f          *FakeSMTP
	from       string
	recipients []string
}

func (s *fakeSMTPSession) AuthPlain(username, password string) error { return nil }
func (s *fakeSMTPSession) Mail(from string, _ *smtp.MailOptions) error {
	s.from = from
	return nil
}
func (s *fakeSMTPSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	s.recipients = append(s.recipients, to)
	return nil
}
func (s *fakeSMTPSession) Data(r io.Reader) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.f.record(SMTPMessage{
		From:       s.from,
		Recipients: append([]string(nil), s.recipients...),
		Data:       body,
	})
	return nil
}
func (s *fakeSMTPSession) Reset() {
	s.from = ""
	s.recipients = nil
}
func (s *fakeSMTPSession) Logout() error { return nil }
