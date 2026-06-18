// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package email_test

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

// smtpConn is a minimal SMTP wire helper used by the test relay. It
// owns a *bufio.Reader/Writer pair that can be replaced after STARTTLS.
type smtpConn struct {
	conn net.Conn
	rw   *bufio.ReadWriter
}

func newSMTPConn(c net.Conn) *smtpConn {
	rw := bufio.NewReadWriter(bufio.NewReader(c), bufio.NewWriter(c))
	return &smtpConn{conn: c, rw: rw}
}

func (s *smtpConn) Close() error {
	return s.conn.Close()
}

func (s *smtpConn) WriteLine(line string) error {
	if _, err := s.rw.WriteString(line + "\r\n"); err != nil {
		return err
	}
	return s.rw.Flush()
}

// WriteEHLO emits a multi-line 250 EHLO response with the given
// extensions. The greeting is on the first line; each extension follows
// on its own continuation line.
func (s *smtpConn) WriteEHLO(greeting string, extensions []string) error {
	lines := append([]string{greeting}, extensions...)
	for i, l := range lines {
		sep := "-"
		if i == len(lines)-1 {
			sep = " "
		}
		if _, err := s.rw.WriteString("250" + sep + l + "\r\n"); err != nil {
			return err
		}
	}
	return s.rw.Flush()
}

func (s *smtpConn) ReadLine() (string, error) {
	line, err := s.rw.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// ReadDATA reads the message body terminated by a line containing only
// a single "." per RFC 5321. Dot-stuffing is reversed.
func (s *smtpConn) ReadDATA() ([]byte, error) {
	var buf strings.Builder
	for {
		line, err := s.rw.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return []byte(buf.String()), nil
			}
			return nil, err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "." {
			break
		}
		// Reverse RFC 5321 §4.5.2 dot-stuffing.
		if strings.HasPrefix(trimmed, "..") {
			trimmed = trimmed[1:]
		}
		buf.WriteString(trimmed)
		buf.WriteString("\r\n")
	}
	return []byte(buf.String()), nil
}

// UpgradeTLS replaces the underlying connection with a TLS server
// connection and rebuilds the buffered reader/writer.
func (s *smtpConn) UpgradeTLS(conf *tls.Config) error {
	tlsConn := tls.Server(s.conn, conf)
	if err := tlsConn.Handshake(); err != nil {
		return err
	}
	s.conn = tlsConn
	s.rw = bufio.NewReadWriter(bufio.NewReader(tlsConn), bufio.NewWriter(tlsConn))
	return nil
}

// generateTestTLS produces an ephemeral self-signed cert/key suitable
// for STARTTLS in tests, plus a CA pool that trusts it.
func generateTestTLS(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
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
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	tlsCert := tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  priv,
		Leaf:        cert,
	}
	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		MinVersion:   tls.VersionTLS12,
	}, pool
}

// base64Encode is a tiny indirection around base64 used by the relay.
func base64Encode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func base64Decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(strings.TrimSpace(s))
}
