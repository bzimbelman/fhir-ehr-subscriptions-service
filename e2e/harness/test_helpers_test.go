// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"io"
	"net"
	"strconv"
	"sync"
	"testing"

	"github.com/emersion/go-smtp"
)

// fakeSMTPRelay is a localhost SMTP server backed by emersion/go-smtp.
// It accepts every RCPT TO and journals nothing; the harness's email
// activator only needs to see a 250 OK on RCPT to classify the probe as
// ProbeAccepted.
type fakeSMTPRelay struct {
	server   *smtp.Server
	listener net.Listener

	mu   sync.Mutex
	host string
	port int
}

// startTestSMTPRelay binds an ephemeral SMTP relay on 127.0.0.1.
func startTestSMTPRelay(t *testing.T) (*fakeSMTPRelay, error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	be := acceptAllBackend{}
	s := smtp.NewServer(be)
	s.Domain = "harness.local"
	s.AllowInsecureAuth = true
	r := &fakeSMTPRelay{server: s, listener: ln, host: host, port: port}
	go func() { _ = s.Serve(ln) }()
	return r, nil
}

func (r *fakeSMTPRelay) Host() string { return r.host }
func (r *fakeSMTPRelay) Port() int    { return r.port }

func (r *fakeSMTPRelay) Close() error {
	_ = r.server.Close()
	return r.listener.Close()
}

type acceptAllBackend struct{}

func (acceptAllBackend) NewSession(_ *smtp.Conn) (smtp.Session, error) {
	return acceptAllSession{}, nil
}

type acceptAllSession struct{}

func (acceptAllSession) AuthPlain(_, _ string) error              { return nil }
func (acceptAllSession) Mail(_ string, _ *smtp.MailOptions) error { return nil }
func (acceptAllSession) Rcpt(_ string, _ *smtp.RcptOptions) error { return nil }
func (acceptAllSession) Data(r io.Reader) error {
	_, _ = io.Copy(io.Discard, r)
	return nil
}
func (acceptAllSession) Reset()        {}
func (acceptAllSession) Logout() error { return nil }
