// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// PROXY protocol v2 (haproxy) header support — N-1.25.
//
// The wire format is documented at
// https://www.haproxy.org/download/2.8/doc/proxy-protocol.txt §2.2.
// We implement the v2-only subset because v1 is a separate ASCII format
// and the scope of N-1.25 is explicitly v2. The parser is hand-rolled
// (rather than pulling in github.com/pires/go-proxyproto) to keep the
// dependency surface lean: the v2 frame layout is small, and the only
// shape we need to extract is "real client TCP/IP address".
//
// Frame layout (network byte order):
//
//	  0      1      2      3      4      5      6      7      8      9     10     11
//	+------+------+------+------+------+------+------+------+------+------+------+------+
//	| 0x0D | 0x0A | 0x0D | 0x0A | 0x00 | 0x0D | 0x0A | 0x51 | 0x55 | 0x49 | 0x54 | 0x0A | signature
//	+------+------+------+------+------+------+------+------+------+------+------+------+
//	|  ver+cmd  | af+xport |     length     |
//	+------+------+------+-----------------+
//	|              <addr block, length bytes>             |
//
// ver+cmd is a single byte: high nibble = version (must be 2), low nibble
// = command (0=LOCAL, 1=PROXY). af+xport is family<<4 | transport.

const proxyV2HeaderTimeoutDefault = 5 * time.Second

var proxyV2Signature = [12]byte{
	0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A,
}

var (
	// errProxyV2BadSignature is returned when the first 12 bytes do not
	// match the v2 signature. Indicates either a non-PROXY peer (e.g., an
	// EHR connecting directly past the LB) or a v1 sender.
	errProxyV2BadSignature = errors.New("mllp: PROXY v2 signature missing")

	// errProxyV2UnsupportedVersion is returned when the version nibble is
	// not 2. v1 has a separate ASCII format and is intentionally not
	// supported here.
	errProxyV2UnsupportedVersion = errors.New("mllp: PROXY header is not v2")

	// errProxyV2UnsupportedFamily is returned for address families other
	// than AF_INET and AF_INET6 with STREAM transport. AF_UNIX, datagram
	// transports, and reserved values fall back to (nil addr, nil err) at
	// the caller's option; we surface them as an explicit error so the
	// listener can reject rather than silently downgrade.
	errProxyV2UnsupportedFamily = errors.New("mllp: PROXY v2 unsupported address family")
)

// readProxyV2Header reads a PROXY protocol v2 header from r and returns
// the real client address it advertises. A v2 LOCAL command (used by LB
// health checks) returns (nil, nil); the caller should fall back to the
// raw socket peer in that case. Any other parse failure returns a
// non-nil error.
//
// The function sets a read deadline on r so a slowloris peer cannot stall
// the accept loop. r.SetReadDeadline must be honored by the underlying
// connection — net.TCPConn and tls.Conn both qualify; net.Pipe is used
// in tests but does not enforce deadlines, so test inputs must drive the
// header in synchronously.
func readProxyV2Header(r net.Conn, timeout time.Duration) (net.Addr, error) {
	if timeout <= 0 {
		timeout = proxyV2HeaderTimeoutDefault
	}
	// Best-effort deadline; if r does not support deadlines (net.Pipe in
	// tests) the call returns nil and we proceed regardless.
	_ = r.SetReadDeadline(time.Now().Add(timeout))
	defer func() {
		_ = r.SetReadDeadline(time.Time{})
	}()

	var sig [12]byte
	if _, err := io.ReadFull(r, sig[:]); err != nil {
		// EOF or partial read with the wrong leading bytes — surface
		// either io.EOF / io.ErrUnexpectedEOF (for clean truncation) or
		// errProxyV2BadSignature for a wrong-but-complete prefix.
		return nil, err
	}
	if sig != proxyV2Signature {
		return nil, errProxyV2BadSignature
	}

	var hdr [4]byte // ver+cmd, af+xport, length(2)
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	verCmd := hdr[0]
	if (verCmd >> 4) != 0x2 {
		return nil, errProxyV2UnsupportedVersion
	}
	cmd := verCmd & 0x0F
	family := hdr[1] >> 4
	transport := hdr[1] & 0x0F
	length := binary.BigEndian.Uint16(hdr[2:4])

	addrBlock := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(r, addrBlock); err != nil {
			return nil, err
		}
	}

	switch cmd {
	case 0x0: // LOCAL — no real client info
		return nil, nil
	case 0x1: // PROXY — extract addresses below
	default:
		return nil, fmt.Errorf("mllp: PROXY v2 unknown command 0x%x", cmd)
	}

	// Only TCP (STREAM) is interesting for our listener.
	if transport != 0x1 {
		return nil, errProxyV2UnsupportedFamily
	}

	switch family {
	case 0x1: // AF_INET — 4+4+2+2 = 12 bytes (TLV trailers permitted)
		if len(addrBlock) < 12 {
			return nil, errProxyV2UnsupportedFamily
		}
		srcIP := net.IPv4(addrBlock[0], addrBlock[1], addrBlock[2], addrBlock[3])
		srcPort := binary.BigEndian.Uint16(addrBlock[8:10])
		return &net.TCPAddr{IP: srcIP, Port: int(srcPort)}, nil

	case 0x2: // AF_INET6 — 16+16+2+2 = 36 bytes (TLV trailers permitted)
		if len(addrBlock) < 36 {
			return nil, errProxyV2UnsupportedFamily
		}
		srcIP := make(net.IP, 16)
		copy(srcIP, addrBlock[0:16])
		srcPort := binary.BigEndian.Uint16(addrBlock[32:34])
		return &net.TCPAddr{IP: srcIP, Port: int(srcPort)}, nil

	default:
		return nil, errProxyV2UnsupportedFamily
	}
}

// proxiedConn wraps a net.Conn so RemoteAddr returns the address parsed
// from the PROXY v2 header rather than the kernel-visible socket peer.
// All other operations pass through unchanged. Used after readProxyV2Header
// returns a non-nil real address; for LOCAL frames (or the disabled
// case) the listener uses the bare net.Conn directly.
type proxiedConn struct {
	net.Conn
	realRemote net.Addr
}

// RemoteAddr returns the address advertised by the PROXY v2 header
// rather than the underlying socket's peer.
func (c *proxiedConn) RemoteAddr() net.Addr {
	return c.realRemote
}

// proxyRejectReason maps a parser error to the metric label value.
func proxyRejectReason(err error) string {
	switch {
	case errors.Is(err, errProxyV2BadSignature):
		return "bad_signature"
	case errors.Is(err, errProxyV2UnsupportedVersion):
		return "unsupported_version"
	case errors.Is(err, errProxyV2UnsupportedFamily):
		return "unsupported_family"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "truncated"
	default:
		return "read_error"
	}
}
