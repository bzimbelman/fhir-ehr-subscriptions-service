// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"
)

// genTestCert returns a self-signed cert + key + CA pool that callers can
// embed into a *tls.Config. The same cert is also added to the returned
// pool so the cert can act as both server cert and trusted client CA in
// mTLS tests.
func genTestCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "mllp-test"},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.IPv6loopback},
		DNSNames:     []string{"localhost"},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatalf("AppendCertsFromPEM: failed")
	}
	return cert, pool
}

func tlsTestListenerConfig(name string, tlsCfg *TLSConfig) ListenerConfig {
	return ListenerConfig{
		Endpoints: []EndpointConfig{
			{Name: name, Bind: "127.0.0.1:0"},
		},
		MaxMessageBytes:    1 << 20,
		ReadIdleTimeout:    5 * time.Second,
		PersistTimeout:     2 * time.Second,
		NackThenDropAfter:  5,
		ShutdownDrainGrace: 2 * time.Second,
		InflightCapPerConn: 64,
		TLS:                tlsCfg,
	}
}

// TestListener_StartsCleartext_NoTLSBlock asserts that a listener with no
// TLS field configured binds successfully (the cleartext path that is
// already in production).
func TestListener_StartsCleartext_NoTLSBlock(t *testing.T) {
	t.Parallel()

	cfg := tlsTestListenerConfig("adt-cleartext", nil)
	l := New(cfg, &fakePersister{}, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := l.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		shutdownCtx, c := context.WithTimeout(context.Background(), 2*time.Second)
		defer c()
		_ = l.Shutdown(shutdownCtx)
	}()

	if l.Addr("adt-cleartext") == nil {
		t.Fatalf("expected bound addr for cleartext endpoint")
	}
}

// TestListener_StartsWithTLSConfig asserts that a listener with a non-nil
// TLS block (server certs only, no mTLS) starts successfully.
func TestListener_StartsWithTLSConfig(t *testing.T) {
	t.Parallel()

	cert, _ := genTestCert(t)
	cfg := tlsTestListenerConfig("adt-tls", &TLSConfig{
		Config: &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		},
	})
	l := New(cfg, &fakePersister{}, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := l.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		shutdownCtx, c := context.WithTimeout(context.Background(), 2*time.Second)
		defer c()
		_ = l.Shutdown(shutdownCtx)
	}()

	if l.Addr("adt-tls") == nil {
		t.Fatalf("expected bound addr for TLS endpoint")
	}
}

// TestListener_StartsWithMTLSConfig asserts that an mTLS listener (with
// ClientCAs and RequireAndVerifyClientCert) starts successfully.
func TestListener_StartsWithMTLSConfig(t *testing.T) {
	t.Parallel()

	cert, pool := genTestCert(t)
	cfg := tlsTestListenerConfig("adt-mtls", &TLSConfig{
		Config: &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		},
		RequireAndVerifyClientCert: true,
		ClientCAs:                  pool,
	})
	l := New(cfg, &fakePersister{}, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := l.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		shutdownCtx, c := context.WithTimeout(context.Background(), 2*time.Second)
		defer c()
		_ = l.Shutdown(shutdownCtx)
	}()

	if l.Addr("adt-mtls") == nil {
		t.Fatalf("expected bound addr for mTLS endpoint")
	}
}
