// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package mockehr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// MLLP framing constants.
const (
	mllpStartBlock byte = 0x0B
	mllpEndBlock   byte = 0x1C
	mllpCR         byte = 0x0D
)

// ackKind selects between the three HL7 v2 acknowledgment outcomes.
type ackKind int

// HL7 acknowledgment outcomes per the v2 spec:
//
//	ackKindApplicationAccept (AA) — message accepted and processed.
//	ackKindApplicationError  (AE) — message rejected for application error.
//	ackKindApplicationReject (AR) — message structurally rejected.
const (
	ackKindApplicationAccept ackKind = iota
	ackKindApplicationError
	ackKindApplicationReject
)

// ErrMessageTooLarge is returned by MLLPClient.Send when the body exceeds
// MaxMessageBytes (after framing overhead, which is 3 bytes).
var ErrMessageTooLarge = errors.New("mockehr: MLLP message exceeds MaxMessageBytes")

// MLLPClient is the EHR-side MLLP sender. One client per remote listener
// address. Send is goroutine-safe.
type MLLPClient struct {
	Addr            string
	DialTimeout     time.Duration
	IOTimeout       time.Duration
	MaxMessageBytes int

	mu sync.Mutex
}

// NewMLLPClient returns a client with sane defaults: 2s dial, 5s IO,
// 1 MiB max message.
func NewMLLPClient(addr string) *MLLPClient {
	return &MLLPClient{
		Addr:            addr,
		DialTimeout:     2 * time.Second,
		IOTimeout:       5 * time.Second,
		MaxMessageBytes: 1 << 20,
	}
}

// Send dials Addr, writes one framed body, reads the listener's ACK frame,
// and returns the unframed ACK body bytes.
//
// The ACK is whatever the listener sent. Send does NOT inspect ACK
// semantics — that is the caller's job. The mocksub-side assertion code
// in the orchestrator looks for MSA|AA|<control id>.
//
// Send is one round trip per call (no MLLP keep-alive multiplexing). The
// MLLP listener LLD allows a single connection to carry multiple messages
// in sequence, but the mock keeps the contract simple.
func (c *MLLPClient) Send(ctx context.Context, body []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.MaxMessageBytes > 0 && len(body) > c.MaxMessageBytes {
		return nil, fmt.Errorf("%w: %d > %d", ErrMessageTooLarge, len(body), c.MaxMessageBytes)
	}

	dialer := net.Dialer{Timeout: c.DialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", c.Addr)
	if err != nil {
		return nil, fmt.Errorf("mockehr: dial %s: %w", c.Addr, err)
	}
	defer conn.Close()

	deadline := time.Now().Add(c.IOTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	if _, err := conn.Write(frameMLLP(body)); err != nil {
		return nil, fmt.Errorf("mockehr: write frame: %w", err)
	}

	buf := make([]byte, 4096)
	var acc bytes.Buffer
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			acc.Write(buf[:n])
		}
		if bytes.Contains(acc.Bytes(), []byte{mllpEndBlock, mllpCR}) {
			break
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("mockehr: read ACK: %w", err)
		}
	}
	ack, err := unframeMLLP(acc.Bytes())
	if err != nil {
		return nil, fmt.Errorf("mockehr: unframe ACK: %w", err)
	}
	return ack, nil
}

// frameMLLP wraps body with START_BLOCK, END_BLOCK, CARRIAGE_RETURN.
func frameMLLP(body []byte) []byte {
	out := make([]byte, 0, len(body)+3)
	out = append(out, mllpStartBlock)
	out = append(out, body...)
	out = append(out, mllpEndBlock, mllpCR)
	return out
}

// unframeMLLP returns the body bytes from a fully-framed MLLP message.
// Returns an error if the frame markers are missing or out of order.
func unframeMLLP(framed []byte) ([]byte, error) {
	startIdx := bytes.IndexByte(framed, mllpStartBlock)
	if startIdx < 0 {
		return nil, errors.New("missing MLLP start block")
	}
	endIdx := bytes.LastIndex(framed, []byte{mllpEndBlock, mllpCR})
	if endIdx < 0 || endIdx <= startIdx {
		return nil, errors.New("missing or misordered MLLP end block")
	}
	return framed[startIdx+1 : endIdx], nil
}

// extractMSH10 returns the MSH-10 (message control id) from the body, or
// "" if the message is malformed. Implementation matches the listener's
// minimal-parse contract: read MSH and take field 10.
func extractMSH10(body []byte) string {
	// First segment up to \r is the MSH segment.
	end := bytes.IndexByte(body, '\r')
	if end < 0 {
		end = len(body)
	}
	msh := string(body[:end])
	parts := strings.Split(msh, "|")
	// MSH|^~\&|app|fac|app|fac|ts||type|ctrl|... — index 9 in 0-based fields.
	// Note: standard HL7 convention treats "MSH" as field 0 with the
	// separator character as the implicit field 1. Splitting on "|" gives:
	//   parts[0] = "MSH"
	//   parts[1] = "^~\&"
	//   parts[2] = sending app (MSH-3)
	//   ...
	//   parts[9] = MSH-10 (control id)
	if len(parts) <= 9 {
		return ""
	}
	return parts[9]
}

// buildACK composes a minimal HL7 ACK message for the given source
// message control id. Same MSH defaults as the builders, MSA-1 = AA/AE/AR.
func buildACK(kind ackKind, ctrlID string) string {
	now := time.Now().UTC()
	msh := buildMSH("ACK", "ACK-"+ctrlID, now)
	code := "AA"
	switch kind {
	case ackKindApplicationError:
		code = "AE"
	case ackKindApplicationReject:
		code = "AR"
	}
	msa := fmt.Sprintf("MSA|%s|%s", code, ctrlID)
	return joinSegments(msh, msa)
}
