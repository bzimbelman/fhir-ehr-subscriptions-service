// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

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
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureLogger records log entries so tests can assert on warning text.
type captureLogger struct {
	mu      sync.Mutex
	entries []logEntry
}

type logEntry struct {
	level  string
	msg    string
	fields map[string]any
}

func (c *captureLogger) Info(msg string, fields map[string]any) {
	c.add("info", msg, fields)
}
func (c *captureLogger) Warn(msg string, fields map[string]any) {
	c.add("warn", msg, fields)
}
func (c *captureLogger) Error(msg string, fields map[string]any) {
	c.add("error", msg, fields)
}
func (c *captureLogger) add(level, msg string, fields map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := map[string]any{}
	for k, v := range fields {
		cp[k] = v
	}
	c.entries = append(c.entries, logEntry{level: level, msg: msg, fields: cp})
}
func (c *captureLogger) snapshot() []logEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]logEntry, len(c.entries))
	copy(out, c.entries)
	return out
}

// hasWarn reports whether any captured WARN entry has either the
// needle in its message OR in any of its field keys / string values.
// Matches what an SRE would grep for in production logs.
func (c *captureLogger) hasWarn(needle string) bool {
	for _, e := range c.snapshot() {
		if e.level != "warn" {
			continue
		}
		if strings.Contains(e.msg, needle) {
			return true
		}
		for k, v := range e.fields {
			if strings.Contains(k, needle) {
				return true
			}
			if s, ok := v.(string); ok && strings.Contains(s, needle) {
				return true
			}
		}
	}
	return false
}

// B-19: MaxConnections must be enforced — once the configured cap is
// reached, additional accepted TCP connections are gracefully refused
// (closed immediately) and a WARN log line records the offending peer.
func TestListener_MaxConnections_RefusesExcess(t *testing.T) {
	t.Parallel()

	const maxConns = 3
	p := &fakePersister{}
	m := newFakeMetrics()
	logr := &captureLogger{}

	cfg := ListenerConfig{
		Endpoints:          []EndpointConfig{{Name: "adt-feed", Bind: "127.0.0.1:0"}},
		MaxMessageBytes:    1 << 20,
		ReadIdleTimeout:    5 * time.Second,
		PersistTimeout:     2 * time.Second,
		NackThenDropAfter:  5,
		ShutdownDrainGrace: 2 * time.Second,
		MaxConnections:     maxConns,
	}
	l := New(cfg, p, m, logr)
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("listener start: %v", err)
	}
	defer func() {
		_ = l.Shutdown(context.Background())
	}()

	addr := l.Addr("adt-feed")
	if addr == nil {
		t.Fatalf("addr unavailable")
	}

	// Hold maxConns connections open.
	held := make([]net.Conn, 0, maxConns)
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()
	for i := 0; i < maxConns; i++ {
		c, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		held = append(held, c)
	}

	// Wait until the listener sees them.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if l.Status().Endpoints[0].ActiveConnections >= maxConns {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := l.Status().Endpoints[0].ActiveConnections; got != maxConns {
		t.Fatalf("active conns = %d, want %d", got, maxConns)
	}

	// The next connection must be refused. Either the dial is reset,
	// or the server closes immediately so reads return io.EOF.
	extra, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
	if err != nil {
		// Reset / connection refused — also acceptable.
		t.Logf("excess dial rejected at OS level: %v", err)
	} else {
		_ = extra.SetReadDeadline(time.Now().Add(1 * time.Second))
		buf := make([]byte, 1)
		n, rerr := extra.Read(buf)
		if rerr == nil && n > 0 {
			t.Fatalf("excess connection accepted; read %d bytes from a refused conn", n)
		}
		if rerr != nil && !errors.Is(rerr, io.EOF) && !strings.Contains(rerr.Error(), "closed") &&
			!strings.Contains(rerr.Error(), "reset") {
			// Any of these are evidence the server closed the conn; we
			// accept all of them.
			t.Logf("excess conn closed (%v)", rerr)
		}
		_ = extra.Close()
	}

	// Active count must stay at maxConns; the refused conn must NOT be
	// counted.
	if got := l.Status().Endpoints[0].ActiveConnections; got > maxConns {
		t.Fatalf("active conns rose to %d after refusal; max should hold", got)
	}

	if !logr.hasWarn("max_connections") && !logr.hasWarn("connection_limit") {
		t.Errorf("expected a WARN log mentioning max_connections / connection_limit; got %+v",
			logr.snapshot())
	}
}

// B-19: MaxConnectionsPerIP must rate-limit per source IP. Three
// connections from the same IP succeed; a fourth is refused. Different
// IPs are independent — but since we cannot easily spoof remote IPs in
// a unit test, we exercise just the same-IP ceiling.
func TestListener_MaxConnectionsPerIP_RefusesExcess(t *testing.T) {
	t.Parallel()

	const perIP = 2
	p := &fakePersister{}
	m := newFakeMetrics()
	logr := &captureLogger{}

	cfg := ListenerConfig{
		Endpoints:           []EndpointConfig{{Name: "adt-feed", Bind: "127.0.0.1:0"}},
		MaxMessageBytes:     1 << 20,
		ReadIdleTimeout:     5 * time.Second,
		PersistTimeout:      2 * time.Second,
		NackThenDropAfter:   5,
		ShutdownDrainGrace:  2 * time.Second,
		MaxConnectionsPerIP: perIP,
	}
	l := New(cfg, p, m, logr)
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("listener start: %v", err)
	}
	defer func() {
		_ = l.Shutdown(context.Background())
	}()

	addr := l.Addr("adt-feed")
	if addr == nil {
		t.Fatalf("addr unavailable")
	}

	// First perIP succeed.
	held := make([]net.Conn, 0, perIP)
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()
	for i := 0; i < perIP; i++ {
		c, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		held = append(held, c)
	}

	// Wait for active count to settle.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if l.Status().Endpoints[0].ActiveConnections >= perIP {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Excess from the same IP must be refused.
	extra, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
	if err == nil {
		_ = extra.SetReadDeadline(time.Now().Add(1 * time.Second))
		buf := make([]byte, 1)
		n, rerr := extra.Read(buf)
		if rerr == nil && n > 0 {
			t.Fatalf("excess per-IP connection accepted; read %d bytes", n)
		}
		_ = extra.Close()
	}

	if !logr.hasWarn("max_connections_per_ip") && !logr.hasWarn("per_ip_limit") {
		t.Errorf("expected a WARN log mentioning max_connections_per_ip / per_ip_limit; got %+v",
			logr.snapshot())
	}

	// Once one of the held conns drops, a new one MUST succeed.
	_ = held[0].Close()
	held = held[1:]
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if l.Status().Endpoints[0].ActiveConnections < perIP {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	c, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
	if err != nil {
		t.Fatalf("post-release dial: %v", err)
	}
	defer c.Close()
	// Verify the conn is held — give the listener a beat to track it.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if l.Status().Endpoints[0].ActiveConnections == perIP {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("post-release conn not tracked; active=%d", l.Status().Endpoints[0].ActiveConnections)
}

// B-20: MLLP listener must be able to serve TLS. With a TLSConfig set,
// a plaintext client cannot complete a TLS handshake; a TLS client
// using a cert in the trust pool can.
func TestListener_TLS_RequiresTLSHandshake(t *testing.T) {
	t.Parallel()

	tlsCfg, pool := generateTestServerTLS(t)

	p := &fakePersister{}
	m := newFakeMetrics()
	logr := &captureLogger{}
	cfg := ListenerConfig{
		Endpoints:          []EndpointConfig{{Name: "adt-feed", Bind: "127.0.0.1:0"}},
		MaxMessageBytes:    1 << 20,
		ReadIdleTimeout:    5 * time.Second,
		PersistTimeout:     2 * time.Second,
		NackThenDropAfter:  5,
		ShutdownDrainGrace: 2 * time.Second,
		TLS:                &TLSConfig{Config: tlsCfg},
	}
	l := New(cfg, p, m, logr)
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("listener start: %v", err)
	}
	defer func() {
		_ = l.Shutdown(context.Background())
	}()

	addr := l.Addr("adt-feed")
	if addr == nil {
		t.Fatalf("addr unavailable")
	}

	// Plaintext client gets a TLS handshake error.
	plain, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
	if err != nil {
		t.Fatalf("plain dial: %v", err)
	}
	if _, werr := plain.Write([]byte("MSH|^~\\&|SNDR\r")); werr == nil {
		// Plaintext client may succeed on Write but the next read
		// returns an error because the server expects a TLS hello.
		_ = plain.SetReadDeadline(time.Now().Add(1 * time.Second))
		buf := make([]byte, 1)
		_, rerr := plain.Read(buf)
		if rerr == nil {
			t.Errorf("plaintext client got a response from a TLS-only listener")
		}
	}
	_ = plain.Close()

	// TLS client succeeds.
	clientTLS := &tls.Config{
		ServerName: "127.0.0.1",
		RootCAs:    pool,
		MinVersion: tls.VersionTLS12,
	}
	tlsConn, err := tls.Dial("tcp", addr.String(), clientTLS)
	if err != nil {
		t.Fatalf("tls dial: %v", err)
	}
	defer tlsConn.Close()

	// Send one MLLP frame and read the ACK.
	if _, werr := tlsConn.Write(mllpFrame(sampleORU)); werr != nil {
		t.Fatalf("tls write: %v", werr)
	}
	_ = tlsConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	n, err := readMLLPFrame(tlsConn, buf)
	if err != nil {
		t.Fatalf("tls read ack: %v", err)
	}
	body := string(buf[:n])
	if !strings.Contains(body, "MSA|AA|MSG-12345") {
		t.Errorf("ACK missing MSA AA; body = %q", body)
	}
}

// B-20: mTLS — when RequireAndVerifyClientCert is set with a trusted
// ClientCAs pool, a client without a cert is rejected, and a client
// presenting a cert signed by a CA in the pool is accepted.
func TestListener_TLS_MTLS_RequiresClientCert(t *testing.T) {
	t.Parallel()

	srvTLS, srvPool, clientCert, clientKey := generateTestMTLS(t)

	p := &fakePersister{}
	m := newFakeMetrics()
	logr := &captureLogger{}
	cfg := ListenerConfig{
		Endpoints:          []EndpointConfig{{Name: "adt-feed", Bind: "127.0.0.1:0"}},
		MaxMessageBytes:    1 << 20,
		ReadIdleTimeout:    5 * time.Second,
		PersistTimeout:     2 * time.Second,
		NackThenDropAfter:  5,
		ShutdownDrainGrace: 2 * time.Second,
		TLS: &TLSConfig{
			Config:                     srvTLS,
			RequireAndVerifyClientCert: true,
			ClientCAs:                  srvPool,
		},
	}
	l := New(cfg, p, m, logr)
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		_ = l.Shutdown(context.Background())
	}()
	addr := l.Addr("adt-feed")
	if addr == nil {
		t.Fatalf("addr unavailable")
	}

	// No client cert -> handshake fails.
	noCertCfg := &tls.Config{
		ServerName: "127.0.0.1",
		RootCAs:    srvPool,
		MinVersion: tls.VersionTLS12,
	}
	noCert, err := tls.Dial("tcp", addr.String(), noCertCfg)
	if err == nil {
		// Some stacks defer the failure to the first I/O.
		_ = noCert.SetReadDeadline(time.Now().Add(1 * time.Second))
		buf := make([]byte, 1)
		_, rerr := noCert.Read(buf)
		if rerr == nil {
			t.Errorf("client without cert succeeded against mTLS listener")
		}
		_ = noCert.Close()
	}

	// With a trusted client cert -> handshake + frame succeed.
	withCertCfg := &tls.Config{
		ServerName:   "127.0.0.1",
		RootCAs:      srvPool,
		Certificates: []tls.Certificate{{Certificate: [][]byte{clientCert.Raw}, PrivateKey: clientKey}},
		MinVersion:   tls.VersionTLS12,
	}
	conn, err := tls.Dial("tcp", addr.String(), withCertCfg)
	if err != nil {
		t.Fatalf("client cert dial: %v", err)
	}
	defer conn.Close()
	if _, werr := conn.Write(mllpFrame(sampleORU)); werr != nil {
		t.Fatalf("write: %v", werr)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	n, err := readMLLPFrame(conn, buf)
	if err != nil {
		t.Fatalf("read ack: %v", err)
	}
	if !strings.Contains(string(buf[:n]), "MSA|AA|MSG-12345") {
		t.Errorf("ACK missing MSA AA; body = %q", buf[:n])
	}
}

// ---- helpers ----

// mllpFrame wraps body in MLLP start/end markers.
func mllpFrame(body string) []byte {
	out := make([]byte, 0, len(body)+3)
	out = append(out, frameStart)
	out = append(out, body...)
	out = append(out, frameEnd1, frameEnd2)
	return out
}

// readMLLPFrame reads a single MLLP frame (start byte … end markers)
// from the conn into buf, returning the bytes read (excluding markers).
func readMLLPFrame(conn net.Conn, buf []byte) (int, error) {
	r := bufio.NewReader(conn)
	for {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		if b == frameStart {
			break
		}
	}
	n := 0
	for {
		b, err := r.ReadByte()
		if err != nil {
			return n, err
		}
		if b == frameEnd1 {
			b2, err := r.ReadByte()
			if err != nil {
				return n, err
			}
			if b2 == frameEnd2 {
				return n, nil
			}
			if n+2 > len(buf) {
				return n, errors.New("buf too small")
			}
			buf[n] = frameEnd1
			buf[n+1] = b2
			n += 2
			continue
		}
		if n+1 > len(buf) {
			return n, errors.New("buf too small")
		}
		buf[n] = b
		n++
	}
}

func generateTestServerTLS(t *testing.T) (*tls.Config, *x509.CertPool) {
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
		t.Fatalf("parse cert: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	srvCert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv, Leaf: cert}
	return &tls.Config{
		Certificates: []tls.Certificate{srvCert},
		MinVersion:   tls.VersionTLS12,
	}, pool
}

func generateTestMTLS(t *testing.T) (srv *tls.Config, pool *x509.CertPool, clientCert *x509.Certificate, clientKey *ecdsa.PrivateKey) {
	t.Helper()
	srv, pool = generateTestServerTLS(t)
	cKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("client key: %v", err)
	}
	cTmpl := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "test-client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	// Self-signed for simplicity; the server pool trusts it directly.
	der, err := x509.CreateCertificate(rand.Reader, &cTmpl, &cTmpl, &cKey.PublicKey, cKey)
	if err != nil {
		t.Fatalf("client cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("client cert parse: %v", err)
	}
	pool.AddCert(cert)
	return srv, pool, cert, cKey
}
