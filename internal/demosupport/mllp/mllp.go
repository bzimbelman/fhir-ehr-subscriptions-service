// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package mllp is the demo CLI's MLLP client — the minimal subset of
// e2e/mockehr the operator-facing demo-publisher needs (OP #158).
// Operator binaries must not import test scaffolding.
package mllp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// MLLP framing constants.
const (
	startBlock byte = 0x0B
	endBlock   byte = 0x1C
	cr         byte = 0x0D
)

// ErrMessageTooLarge is returned by Client.Send when body exceeds
// MaxMessageBytes.
var ErrMessageTooLarge = errors.New("mllp: message exceeds MaxMessageBytes")

// Client dials a remote MLLP listener and exchanges one framed
// message per call. Send is goroutine-safe.
type Client struct {
	Addr            string
	DialTimeout     time.Duration
	IOTimeout       time.Duration
	MaxMessageBytes int

	mu sync.Mutex
}

// NewClient returns a client with sane defaults: 2s dial, 5s IO,
// 1 MiB max message.
func NewClient(addr string) *Client {
	return &Client{
		Addr:            addr,
		DialTimeout:     2 * time.Second,
		IOTimeout:       5 * time.Second,
		MaxMessageBytes: 1 << 20,
	}
}

// Send dials Addr, writes one framed body, reads the listener's ACK
// frame, and returns the unframed ACK bytes.
func (c *Client) Send(ctx context.Context, body []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.MaxMessageBytes > 0 && len(body) > c.MaxMessageBytes {
		return nil, fmt.Errorf("%w: %d > %d", ErrMessageTooLarge, len(body), c.MaxMessageBytes)
	}

	dialer := net.Dialer{Timeout: c.DialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", c.Addr)
	if err != nil {
		return nil, fmt.Errorf("mllp: dial %s: %w", c.Addr, err)
	}
	defer conn.Close()

	deadline := time.Now().Add(c.IOTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	if _, writeErr := conn.Write(frame(body)); writeErr != nil {
		return nil, fmt.Errorf("mllp: write frame: %w", writeErr)
	}

	buf := make([]byte, 4096)
	var acc bytes.Buffer
	for {
		n, readErr := conn.Read(buf)
		if n > 0 {
			acc.Write(buf[:n])
		}
		if bytes.Contains(acc.Bytes(), []byte{endBlock, cr}) {
			break
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return nil, fmt.Errorf("mllp: read ACK: %w", readErr)
		}
	}
	ack, err := unframe(acc.Bytes())
	if err != nil {
		return nil, fmt.Errorf("mllp: unframe ACK: %w", err)
	}
	return ack, nil
}

func frame(body []byte) []byte {
	out := make([]byte, 0, len(body)+3)
	out = append(out, startBlock)
	out = append(out, body...)
	out = append(out, endBlock, cr)
	return out
}

func unframe(framed []byte) ([]byte, error) {
	startIdx := bytes.IndexByte(framed, startBlock)
	if startIdx < 0 {
		return nil, errors.New("missing MLLP start block")
	}
	endIdx := bytes.LastIndex(framed, []byte{endBlock, cr})
	if endIdx < 0 || endIdx <= startIdx {
		return nil, errors.New("missing or misordered MLLP end block")
	}
	return framed[startIdx+1 : endIdx], nil
}
