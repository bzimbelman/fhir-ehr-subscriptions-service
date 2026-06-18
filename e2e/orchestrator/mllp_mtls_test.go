// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"crypto/tls"
	"strings"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/mllp"
)

// TestE2E_MLLP_MTLS_RequiresClientCert verifies B-20 mTLS:
// RequireAndVerifyClientCert + ClientCAs forces the client to present
// a certificate signed by a CA in the trust pool. A client without a
// cert is rejected; a client presenting a trusted cert proceeds to
// MLLP exchange.
func TestE2E_MLLP_MTLS_RequiresClientCert(t *testing.T) {
	t.Parallel()

	srvTLS, srvPool, clientCert, clientKey := generateE2EMTLS(t)
	cfg := mllp.ListenerConfig{
		Endpoints:          []mllp.EndpointConfig{{Name: "adt-feed", Bind: "127.0.0.1:0"}},
		MaxMessageBytes:    1 << 20,
		ReadIdleTimeout:    5 * time.Second,
		PersistTimeout:     2 * time.Second,
		NackThenDropAfter:  5,
		ShutdownDrainGrace: 2 * time.Second,
		TLS: &mllp.TLSConfig{
			Config:                     srvTLS,
			RequireAndVerifyClientCert: true,
			ClientCAs:                  srvPool,
		},
	}
	l := mllp.New(cfg, &e2eFakePersister{}, nil, newE2EMLLPLogger())
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = l.Shutdown(context.Background()) }()
	addr := l.Addr("adt-feed").String()

	// No client cert -> handshake fails (or first I/O fails).
	noCert := &tls.Config{
		ServerName: "127.0.0.1",
		RootCAs:    srvPool,
		MinVersion: tls.VersionTLS12,
	}
	conn, err := tls.Dial("tcp", addr, noCert)
	if err == nil {
		_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		buf := make([]byte, 1)
		if _, rerr := conn.Read(buf); rerr == nil {
			t.Errorf("client without cert succeeded against mTLS listener")
		}
		_ = conn.Close()
	}

	// With a trusted client cert -> handshake + frame succeed.
	withCert := &tls.Config{
		ServerName:   "127.0.0.1",
		RootCAs:      srvPool,
		Certificates: []tls.Certificate{{Certificate: [][]byte{clientCert.Raw}, PrivateKey: clientKey}},
		MinVersion:   tls.VersionTLS12,
	}
	c, err := tls.Dial("tcp", addr, withCert)
	if err != nil {
		t.Fatalf("client cert dial: %v", err)
	}
	defer c.Close()
	if _, werr := c.Write(e2eMLLPFrame(sampleORU)); werr != nil {
		t.Fatalf("write: %v", werr)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	body, err := readE2EMLLPFrame(c)
	if err != nil {
		t.Fatalf("read ack: %v", err)
	}
	if !strings.Contains(string(body), "MSA|AA|MSG-12345") {
		t.Errorf("ACK missing MSA AA: %q", body)
	}
}
