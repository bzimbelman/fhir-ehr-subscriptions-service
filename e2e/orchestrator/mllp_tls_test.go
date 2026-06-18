// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/mllp"
)

// TestE2E_MLLP_TLS_PlaintextRejected verifies B-20: configuring
// cfg.TLS makes the listener refuse plaintext connections; a TLS
// client succeeds end-to-end against a sample HL7 ORU message.
func TestE2E_MLLP_TLS_PlaintextRejected(t *testing.T) {
	t.Parallel()

	srvTLS, pool := generateE2EServerTLS(t)
	cfg := mllp.ListenerConfig{
		Endpoints:          []mllp.EndpointConfig{{Name: "adt-feed", Bind: "127.0.0.1:0"}},
		MaxMessageBytes:    1 << 20,
		ReadIdleTimeout:    5 * time.Second,
		PersistTimeout:     2 * time.Second,
		NackThenDropAfter:  5,
		ShutdownDrainGrace: 2 * time.Second,
		TLS:                &mllp.TLSConfig{Config: srvTLS},
	}
	l := mllp.New(cfg, &e2eFakePersister{}, nil, newE2EMLLPLogger())
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = l.Shutdown(context.Background()) }()
	addr := l.Addr("adt-feed").String()

	// Plaintext client gets stonewalled at the first I/O.
	plain, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("plain dial: %v", err)
	}
	_ = plain.SetReadDeadline(time.Now().Add(1 * time.Second))
	if _, werr := plain.Write([]byte("MSH|^~\\&|SNDR\r")); werr == nil {
		buf := make([]byte, 1)
		if _, rerr := plain.Read(buf); rerr == nil {
			t.Errorf("plaintext client got a response from a TLS-only listener")
		}
	}
	_ = plain.Close()

	// TLS client succeeds.
	clientCfg := &tls.Config{
		ServerName: "127.0.0.1",
		RootCAs:    pool,
		MinVersion: tls.VersionTLS12,
	}
	conn, err := tls.Dial("tcp", addr, clientCfg)
	if err != nil {
		t.Fatalf("tls dial: %v", err)
	}
	defer conn.Close()

	if _, werr := conn.Write(e2eMLLPFrame(sampleORU)); werr != nil {
		t.Fatalf("tls write: %v", werr)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	body, err := readE2EMLLPFrame(conn)
	if err != nil {
		t.Fatalf("tls read ack: %v", err)
	}
	if !strings.Contains(string(body), "MSA|AA|MSG-12345") {
		t.Errorf("ACK missing MSA AA: %q", body)
	}
}

// --- helpers shared by mllp e2e tests ---

const (
	sampleORU = "MSH|^~\\&|SNDR|FAC|RCVR|RFAC|20240101010101||ORU^R01|MSG-12345|P|2.5\rPID|||x\r"
)

// e2eMLLPFrame wraps body in MLLP start/end markers.
func e2eMLLPFrame(body string) []byte {
	out := make([]byte, 0, len(body)+3)
	out = append(out, 0x0B)
	out = append(out, body...)
	out = append(out, 0x1C, 0x0D)
	return out
}

// readE2EMLLPFrame reads a single MLLP frame from conn.
func readE2EMLLPFrame(conn net.Conn) ([]byte, error) {
	r := bufio.NewReader(conn)
	for {
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if b == 0x0B {
			break
		}
	}
	var buf []byte
	for {
		b, err := r.ReadByte()
		if err != nil {
			return buf, err
		}
		if b == 0x1C {
			b2, err := r.ReadByte()
			if err != nil {
				return buf, err
			}
			if b2 == 0x0D {
				return buf, nil
			}
			buf = append(buf, 0x1C, b2)
			continue
		}
		buf = append(buf, b)
	}
}

func generateE2EServerTLS(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"127.0.0.1"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: priv, Leaf: cert}},
		MinVersion:   tls.VersionTLS12,
	}, pool
}

func generateE2EMTLS(t *testing.T) (*tls.Config, *x509.CertPool, *x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	srvTLS, pool := generateE2EServerTLS(t)
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("client key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "test-client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("client cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	pool.AddCert(cert)
	return srvTLS, pool, cert, priv
}

// Suppress "imported and not used" for errors when no test in this
// file references it directly.
var _ = errors.New
