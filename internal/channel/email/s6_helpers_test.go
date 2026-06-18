// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package email_test

import (
	"crypto/tls"
	"net"
	"sync"
	"testing"
)

// testTLSConfigFor builds a TLS config that trusts the test relay's cert.
func testTLSConfigFor(srv *testRelay) *tls.Config {
	return &tls.Config{
		ServerName: srv.Host,
		RootCAs:    srv.RootCAs,
		MinVersion: tls.VersionTLS12,
	}
}

// hungListener accepts TCP connections but never writes any data, so any
// SMTP banner read will block. Used to verify the dialer applies a timeout
// even when the envelope carries no deadline.
type hungListener struct {
	host string
	port int
	l    net.Listener
	wg   sync.WaitGroup
}

func startHungListener(t *testing.T) *hungListener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().(*net.TCPAddr)
	h := &hungListener{
		host: "127.0.0.1",
		port: addr.Port,
		l:    l,
	}
	h.wg.Add(1)
	go h.serve()
	return h
}

func (h *hungListener) serve() {
	defer h.wg.Done()
	for {
		c, err := h.l.Accept()
		if err != nil {
			return
		}
		// Hold the connection so it doesn't auto-close.
		go func(c net.Conn) {
			buf := make([]byte, 1024)
			for {
				if _, err := c.Read(buf); err != nil {
					_ = c.Close()
					return
				}
			}
		}(c)
	}
}

func (h *hungListener) stop() {
	_ = h.l.Close()
	h.wg.Wait()
}
