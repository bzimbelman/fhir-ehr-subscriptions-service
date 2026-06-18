// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// readFullProxyFrame reads from conn until the MLLP end markers appear.
// Local helper because the equivalent in integration_test.go is gated
// behind a build tag.
func readFullProxyFrame(t *testing.T, conn net.Conn, timeout time.Duration) []byte {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	buf := make([]byte, 0, 256)
	one := make([]byte, 1)
	for {
		n, err := conn.Read(one)
		if err != nil {
			t.Fatalf("read: %v (so far %q)", err, buf)
		}
		if n == 0 {
			continue
		}
		buf = append(buf, one[0])
		if len(buf) >= 3 && buf[0] == frameStart && buf[len(buf)-2] == frameEnd1 && buf[len(buf)-1] == frameEnd2 {
			return buf
		}
	}
}

// proxyV2Header builds a PROXY protocol v2 PROXY-command frame for IPv4 TCP
// (AF_INET / STREAM, address family + transport packed as 0x11). It is the
// shape a TCP load balancer like AWS NLB or HAProxy emits ahead of the
// real client bytes.
func proxyV2Header(srcIP, dstIP [4]byte, srcPort, dstPort uint16) []byte {
	var buf bytes.Buffer
	// 12-byte signature
	buf.Write([]byte{0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A})
	// version+command (0x21 = v2 + PROXY)
	buf.WriteByte(0x21)
	// address family + transport (0x11 = AF_INET + STREAM)
	buf.WriteByte(0x11)
	// length of address block (12 bytes for IPv4: 4+4+2+2)
	_ = binary.Write(&buf, binary.BigEndian, uint16(12))
	// addresses
	buf.Write(srcIP[:])
	buf.Write(dstIP[:])
	_ = binary.Write(&buf, binary.BigEndian, srcPort)
	_ = binary.Write(&buf, binary.BigEndian, dstPort)
	return buf.Bytes()
}

// proxyV2HeaderLocal builds a v2 LOCAL-command frame (used by health checks
// from the LB itself; carries no client info — listener should fall back
// to the raw socket peer).
func proxyV2HeaderLocal() []byte {
	return []byte{
		0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A,
		0x20,       // v2 + LOCAL
		0x00,       // AF_UNSPEC + UNSPEC
		0x00, 0x00, // length 0
	}
}

// TestProxyV2_ParsesIPv4Header drives the pure-function parser against an
// in-memory pipe. The listener wrapper must, when ProxyProtocolV2 is
// enabled and a well-formed v2 PROXY header is present, surface the
// real client address (here 192.0.2.7:51234) rather than the underlying
// socket peer.
func TestProxyV2_ParsesIPv4Header(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()

	hdr := proxyV2Header([4]byte{192, 0, 2, 7}, [4]byte{10, 0, 0, 1}, 51234, 2575)
	go func() {
		_, _ = client.Write(hdr)
	}()

	addr, err := readProxyV2Header(server, 2*time.Second)
	if err != nil {
		t.Fatalf("readProxyV2Header: %v", err)
	}
	if addr == nil {
		t.Fatalf("expected non-nil real address")
	}
	if addr.String() != "192.0.2.7:51234" {
		t.Fatalf("addr = %q, want 192.0.2.7:51234", addr.String())
	}
}

// TestProxyV2_LocalCommandReturnsNil — LOCAL frames carry no client info.
// The parser returns (nil, nil) so the caller falls back to socket peer.
func TestProxyV2_LocalCommandReturnsNil(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()

	go func() { _, _ = client.Write(proxyV2HeaderLocal()) }()

	addr, err := readProxyV2Header(server, 2*time.Second)
	if err != nil {
		t.Fatalf("readProxyV2Header: %v", err)
	}
	if addr != nil {
		t.Fatalf("LOCAL command must yield nil addr; got %v", addr)
	}
}

// TestProxyV2_BadSignatureRejected — when ProxyProtocolV2 is enabled the
// parser MUST reject anything that does not start with the 12-byte v2
// signature. Returning success on garbage would let an attacker spoof a
// peer address by writing arbitrary bytes upstream of the LB.
func TestProxyV2_BadSignatureRejected(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()

	go func() {
		// 16 bytes of HL7 — looks like it could be the start of a frame
		// but is not a v2 signature.
		_, _ = client.Write([]byte("\x0bMSH|^~\\&|FOO|"))
	}()

	_, err := readProxyV2Header(server, 2*time.Second)
	if err == nil {
		t.Fatalf("expected error for missing v2 signature")
	}
	if !errors.Is(err, errProxyV2BadSignature) {
		t.Fatalf("err = %v, want errProxyV2BadSignature", err)
	}
}

// TestProxyV2_TruncatedHeaderRejected — partial signature followed by EOF
// must surface as an error, not a hang or success.
func TestProxyV2_TruncatedHeaderRejected(t *testing.T) {
	server, client := net.Pipe()

	go func() {
		// First six bytes of the v2 signature, then close.
		_, _ = client.Write([]byte{0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D})
		_ = client.Close()
	}()

	_, err := readProxyV2Header(server, 2*time.Second)
	if err == nil {
		t.Fatalf("expected error for truncated header")
	}
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, errProxyV2BadSignature) {
		t.Fatalf("err = %v, want io.EOF / ErrUnexpectedEOF / errProxyV2BadSignature", err)
	}
}

// TestProxyV2_UnsupportedVersionRejected — v1 headers (or any non-v2
// version nibble) must be rejected. We deliberately scope this story to
// v2 only; v1 is a separate, parser-distinct format.
func TestProxyV2_UnsupportedVersionRejected(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()

	hdr := proxyV2Header([4]byte{1, 2, 3, 4}, [4]byte{5, 6, 7, 8}, 1, 1)
	// Tamper version nibble: replace 0x21 (v2 + PROXY) with 0x11 (v1 + PROXY).
	hdr[12] = 0x11
	go func() { _, _ = client.Write(hdr) }()

	_, err := readProxyV2Header(server, 2*time.Second)
	if err == nil {
		t.Fatalf("expected error for non-v2 version")
	}
	if !errors.Is(err, errProxyV2UnsupportedVersion) {
		t.Fatalf("err = %v, want errProxyV2UnsupportedVersion", err)
	}
}

// TestProxyV2_IPv6Header — AF_INET6 / STREAM (0x21) must also work; the
// real client address is rendered as [v6]:port.
func TestProxyV2_IPv6Header(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()

	var buf bytes.Buffer
	buf.Write([]byte{0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A})
	buf.WriteByte(0x21)
	buf.WriteByte(0x21) // AF_INET6 + STREAM
	_ = binary.Write(&buf, binary.BigEndian, uint16(36))
	src := net.ParseIP("2001:db8::1").To16()
	dst := net.ParseIP("2001:db8::2").To16()
	buf.Write(src)
	buf.Write(dst)
	_ = binary.Write(&buf, binary.BigEndian, uint16(60001))
	_ = binary.Write(&buf, binary.BigEndian, uint16(2575))

	go func() { _, _ = client.Write(buf.Bytes()) }()

	addr, err := readProxyV2Header(server, 2*time.Second)
	if err != nil {
		t.Fatalf("readProxyV2Header: %v", err)
	}
	if addr == nil {
		t.Fatalf("expected non-nil v6 address")
	}
	want := "[2001:db8::1]:60001"
	if addr.String() != want {
		t.Fatalf("addr = %q, want %q", addr.String(), want)
	}
}

// TestProxyV2_TLVTrailerSkipped — v2 allows arbitrary TLV bytes after the
// address block (PP2_TYPE_AWS, PP2_TYPE_CRC32C, etc.). The address-block
// length covers all of that, so the parser MUST consume exactly
// `length` bytes and leave the connection positioned at the first
// post-header byte.
func TestProxyV2_TLVTrailerSkipped(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()

	var buf bytes.Buffer
	buf.Write([]byte{0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A})
	buf.WriteByte(0x21)
	buf.WriteByte(0x11)
	// length = 12 (IPv4 addrs) + 8 (TLV) = 20
	_ = binary.Write(&buf, binary.BigEndian, uint16(20))
	buf.Write([]byte{192, 0, 2, 9})
	buf.Write([]byte{10, 0, 0, 1})
	_ = binary.Write(&buf, binary.BigEndian, uint16(40000))
	_ = binary.Write(&buf, binary.BigEndian, uint16(2575))
	// 8 bytes of TLV: type=0xEA (AWS) + length 0x0005 + 5 bytes of opaque
	buf.Write([]byte{0xEA, 0x00, 0x05, 'a', 'b', 'c', 'd', 'e'})

	// After the header, send a few real bytes the listener should see.
	hdr := buf.Bytes()
	payload := []byte("HELLO")
	go func() {
		_, _ = client.Write(hdr)
		_, _ = client.Write(payload)
	}()

	addr, err := readProxyV2Header(server, 2*time.Second)
	if err != nil {
		t.Fatalf("readProxyV2Header: %v", err)
	}
	if addr == nil || addr.String() != "192.0.2.9:40000" {
		t.Fatalf("addr = %v, want 192.0.2.9:40000", addr)
	}
	// The next read must surface "HELLO" — the parser must not have over-read.
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(server, got); err != nil {
		t.Fatalf("read after header: %v", err)
	}
	if string(got) != "HELLO" {
		t.Fatalf("post-header read = %q, want HELLO", got)
	}
}

// TestListener_ProxyV2_Enabled_RealIPSurfaced — the real-listener
// integration test mandated by the story: with ProxyProtocolV2 enabled
// on an endpoint, a real TCP client that prepends a v2 PROXY header MUST
// have the real client address recorded on the persisted row, not the
// loopback peer the kernel sees.
func TestListener_ProxyV2_Enabled_RealIPSurfaced(t *testing.T) {
	p := &fakePersister{}
	m := newFakeMetrics()

	cfg := ListenerConfig{
		Endpoints: []EndpointConfig{
			{Name: "adt-feed", Bind: "127.0.0.1:0", ProxyProtocolV2: true},
		},
		MaxMessageBytes:    1 << 20,
		ReadIdleTimeout:    5 * time.Second,
		PersistTimeout:     2 * time.Second,
		NackThenDropAfter:  3,
		ShutdownDrainGrace: 2 * time.Second,
		InflightCapPerConn: 8,
	}

	l := New(cfg, p, m, nil)
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("listener start: %v", err)
	}
	defer func() { _ = l.Shutdown(context.Background()) }()

	addr := l.Addr("adt-feed")
	if addr == nil {
		t.Fatalf("listener did not bind")
	}

	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Pretend we are a load balancer in front of the listener: send the
	// v2 PROXY header advertising 198.51.100.42:51000 as the real client.
	if _, err := conn.Write(proxyV2Header(
		[4]byte{198, 51, 100, 42}, [4]byte{10, 0, 0, 1}, 51000, 2575)); err != nil {
		t.Fatalf("write proxy header: %v", err)
	}

	body := "MSH|^~\\&|SNDR|FAC|RCVR|RFAC|20240101010101||ADT^A01|PROXY-MSG-1|P|2.5\rPID|||x\r"
	if _, err := conn.Write(frameBytes(body)); err != nil {
		t.Fatalf("write frame: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(p.Rows()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	rows := p.Rows()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].PeerAddr != "198.51.100.42:51000" {
		t.Fatalf("PeerAddr = %q, want 198.51.100.42:51000 (real client behind PROXY header)", rows[0].PeerAddr)
	}
}

// TestListener_ProxyV2_Enabled_MissingHeaderRejected — when the operator
// turned PROXY v2 ON, a client that does NOT send a header must be
// dropped before any HL7 bytes are processed and a metric logged.
func TestListener_ProxyV2_Enabled_MissingHeaderRejected(t *testing.T) {
	p := &fakePersister{}
	m := newFakeMetrics()

	cfg := ListenerConfig{
		Endpoints: []EndpointConfig{
			{Name: "adt-feed", Bind: "127.0.0.1:0", ProxyProtocolV2: true},
		},
		MaxMessageBytes:    1 << 20,
		ReadIdleTimeout:    1 * time.Second,
		PersistTimeout:     2 * time.Second,
		NackThenDropAfter:  3,
		ShutdownDrainGrace: 2 * time.Second,
		InflightCapPerConn: 8,
	}

	l := New(cfg, p, m, nil)
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("listener start: %v", err)
	}
	defer func() { _ = l.Shutdown(context.Background()) }()

	conn, err := net.Dial("tcp", l.Addr("adt-feed").String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Send a regular HL7 frame WITHOUT a PROXY header. Listener must drop.
	body := "MSH|^~\\&|SNDR|FAC|RCVR|RFAC|20240101||ADT^A01|MSG-NOPROX|P|2.5\rPID|||x\r"
	if _, err := conn.Write(frameBytes(body)); err != nil {
		// pipe broken on the listener side is acceptable
		if !strings.Contains(err.Error(), "broken pipe") && !strings.Contains(err.Error(), "closed") {
			t.Fatalf("write frame: %v", err)
		}
	}

	// The listener should close the conn before processing — read returns
	// EOF (or a reset) and no row gets persisted.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 256)
	_, _ = conn.Read(buf) // ignored; we expect EOF or empty

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if got := m.counter(MetricProxyHeaderRejectedTotal, map[string]string{
			"listener_endpoint": "adt-feed",
			"reason":            "bad_signature",
		}); got >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if rows := p.Rows(); len(rows) != 0 {
		t.Fatalf("rows = %d, want 0 (proxy header missing — connection must be dropped before persist)", len(rows))
	}
	if got := m.counter(MetricProxyHeaderRejectedTotal, map[string]string{
		"listener_endpoint": "adt-feed",
		"reason":            "bad_signature",
	}); got != 1 {
		t.Fatalf("proxy_header_rejected_total{reason=bad_signature} = %v, want 1", got)
	}
}

// TestListener_ProxyV2_Disabled_HeaderNotParsed — when PROXY v2 is disabled
// (the default) and a client sends header bytes anyway, the listener
// MUST NOT secretly parse them. Concretely: a peer that prepends a v2
// header advertising a spoofed source IP, then sends a real HL7 frame,
// must persist with the loopback peer address — not the spoofed one.
// The framer treats the header as pre-frame noise (per LLD §5.4: bytes
// before 0x0B are discarded).
func TestListener_ProxyV2_Disabled_HeaderNotParsed(t *testing.T) {
	p := &fakePersister{}
	m := newFakeMetrics()

	cfg := ListenerConfig{
		Endpoints: []EndpointConfig{
			{Name: "adt-feed", Bind: "127.0.0.1:0"}, // ProxyProtocolV2 NOT set
		},
		MaxMessageBytes:    1 << 20,
		ReadIdleTimeout:    5 * time.Second,
		PersistTimeout:     2 * time.Second,
		NackThenDropAfter:  3,
		ShutdownDrainGrace: 2 * time.Second,
		InflightCapPerConn: 8,
	}

	l := New(cfg, p, m, nil)
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("listener start: %v", err)
	}
	defer func() { _ = l.Shutdown(context.Background()) }()

	conn, err := net.Dial("tcp", l.Addr("adt-feed").String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Prepend a spoofed PROXY v2 header, then send a real frame.
	if _, err := conn.Write(proxyV2Header(
		[4]byte{198, 51, 100, 42}, [4]byte{10, 0, 0, 1}, 51000, 2575)); err != nil {
		t.Fatalf("write header: %v", err)
	}
	body := "MSH|^~\\&|SNDR|FAC|RCVR|RFAC|20240101010101||ADT^A01|MSG-NPDIS|P|2.5\rPID|||x\r"
	if _, err := conn.Write(frameBytes(body)); err != nil {
		t.Fatalf("write frame: %v", err)
	}

	// Read the ACK so we know the listener processed the frame.
	ack := readFullProxyFrame(t, conn, 3*time.Second)
	if !strings.Contains(string(ack), "MSA|AA|MSG-NPDIS") {
		t.Fatalf("expected AA ACK; got %q", ack)
	}

	rows := p.Rows()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	// The persisted PeerAddr must be the loopback peer assigned by the
	// kernel, NOT the spoofed 198.51.100.42 from the header.
	if strings.HasPrefix(rows[0].PeerAddr, "198.51.100.42") {
		t.Fatalf("PeerAddr leaked spoofed addr %q — listener silently parsed PROXY header despite flag being off", rows[0].PeerAddr)
	}
	if !strings.HasPrefix(rows[0].PeerAddr, "127.0.0.1:") && !strings.HasPrefix(rows[0].PeerAddr, "[::1]:") {
		t.Fatalf("PeerAddr = %q; expected loopback peer", rows[0].PeerAddr)
	}
	// Sanity: no proxy-rejected metric counted (the feature is off).
	if got := m.counter(MetricProxyHeaderRejectedTotal, map[string]string{
		"listener_endpoint": "adt-feed",
		"reason":            "bad_signature",
	}); got != 0 {
		t.Fatalf("proxy_header_rejected_total = %v, want 0 when feature disabled", got)
	}
}
