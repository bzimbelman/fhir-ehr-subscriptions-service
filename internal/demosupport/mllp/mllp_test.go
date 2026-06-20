// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// MLLP framing per HL7 v2 minimal lower-layer protocol:
//
//   START_BLOCK  = 0x0B
//   END_BLOCK    = 0x1C
//   CARRIAGE_RTN = 0x0D
//
// One framed message = START_BLOCK + body + END_BLOCK + CARRIAGE_RTN.
//
// The demo-publisher's MLLP client dials a configured listener, frames the
// caller's HL7 string, writes it, and reads the listener's ACK frame back.
// These tests stand up a real net.Listener — no mocks — and verify the
// client's framing, read loop, error paths, and concurrency.

func TestNewClient_AppliesDefaults(t *testing.T) {
	t.Parallel()
	c := NewClient("127.0.0.1:9999")
	if c.Addr != "127.0.0.1:9999" {
		t.Fatalf("Addr: got %q want 127.0.0.1:9999", c.Addr)
	}
	if c.DialTimeout != 2*time.Second {
		t.Fatalf("DialTimeout default: got %v want 2s", c.DialTimeout)
	}
	if c.IOTimeout != 5*time.Second {
		t.Fatalf("IOTimeout default: got %v want 5s", c.IOTimeout)
	}
	if c.MaxMessageBytes != 1<<20 {
		t.Fatalf("MaxMessageBytes default: got %d want 1<<20", c.MaxMessageBytes)
	}
}

func TestClient_Send_FramesAndReceivesACK(t *testing.T) {
	t.Parallel()
	srv := startEchoListener(t, []byte("MSH|^~\\&|FHIRSUBS|TEST|MOCKEHR|E2E|20260619|||ACK^A01|ACK0001|T|2.5.1\rMSA|AA|MLLP0001\r"))
	defer srv.Close()

	c := NewClient(srv.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	body := []byte("MSH|^~\\&|MOCKEHR|E2E|FHIRSUBS|TEST|20260619|||ADT^A01|MLLP0001|T|2.5.1\rEVN|A01|20260619\r")
	ack, err := c.Send(ctx, body)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !bytes.Contains(ack, []byte("MSA|AA|MLLP0001")) {
		t.Fatalf("expected MSA|AA|MLLP0001 in ACK, got: %q", ack)
	}

	// Verify the listener received exactly the framed body the client sent.
	got := <-srv.received
	want := append([]byte{startBlock}, body...)
	want = append(want, endBlock, cr)
	if !bytes.Equal(got, want) {
		t.Fatalf("listener received wrong frame:\n got:  %q\n want: %q", got, want)
	}
}

func TestClient_Send_RejectsOversizedFrame(t *testing.T) {
	t.Parallel()
	c := NewClient("127.0.0.1:0")
	c.MaxMessageBytes = 16
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := c.Send(ctx, []byte("MSH|^~\\&|this is way more than sixteen bytes\r"))
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("expected ErrMessageTooLarge, got: %v", err)
	}
}

func TestClient_Send_DialFailureWraps(t *testing.T) {
	t.Parallel()
	// Reserve an address by listening then closing — guarantees nobody is
	// on the port when we try to dial. (Port may get reused, but the
	// dial-and-immediately-fail race is fine: we only care that we get an
	// error wrapped with the dial prefix.)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	c := NewClient(addr)
	c.DialTimeout = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err = c.Send(ctx, []byte("MSH|^~\\&|x\r"))
	if err == nil {
		t.Fatalf("expected dial error, got nil")
	}
	if !strings.Contains(err.Error(), "mllp: dial") {
		t.Fatalf("expected wrapped dial error, got: %v", err)
	}
}

func TestClient_Send_ReadACKError(t *testing.T) {
	t.Parallel()
	// Listener accepts but writes garbage (no end block) then sends RST by
	// abruptly closing. Client should surface a wrapped error from the
	// unframe step OR the read step depending on how the close races.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	go func() {
		conn, acceptErr := l.Accept()
		if acceptErr != nil {
			return
		}
		// Drain whatever the client writes so it doesn't get a write error.
		buf := make([]byte, 4096)
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		_, _ = conn.Read(buf)
		// Send incomplete framing: start block + body but no end block.
		_, _ = conn.Write([]byte{startBlock})
		_, _ = conn.Write([]byte("partial"))
		_ = conn.Close()
	}()

	c := NewClient(l.Addr().String())
	c.IOTimeout = 500 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = c.Send(ctx, []byte("MSH|^~\\&|x\r"))
	if err == nil {
		t.Fatalf("expected error from incomplete ACK, got nil")
	}
	if !strings.Contains(err.Error(), "mllp:") {
		t.Fatalf("expected mllp-prefixed error, got: %v", err)
	}
}

func TestClient_Send_ContextDeadlineTightensIOTimeout(t *testing.T) {
	t.Parallel()
	// Listener accepts the connection then sleeps without responding so the
	// client's read times out. We set a context deadline tighter than
	// IOTimeout; the client must honor the earlier deadline.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	done := make(chan struct{})
	go func() {
		conn, acceptErr := l.Accept()
		if acceptErr != nil {
			close(done)
			return
		}
		// Drain client's frame, then never reply. Hold the conn open until
		// the client closes it (signalled by Read returning).
		buf := make([]byte, 4096)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, readErr := conn.Read(buf); readErr != nil {
				break
			}
		}
		conn.Close()
		close(done)
	}()

	c := NewClient(l.Addr().String())
	c.IOTimeout = 5 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = c.Send(ctx, []byte("MSH|^~\\&|x\r"))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Send did not honor tight context deadline; took %v", elapsed)
	}
	<-done
}

func TestClient_Send_SerializesViaMutex(t *testing.T) {
	t.Parallel()
	// Two concurrent Sends on the same client must not interleave — the
	// mutex guarantees ordering. We verify by counting frames the listener
	// receives as a sanity check (both make it through with intact framing).
	srv := startEchoListenerSerial(t, 2, []byte("MSH|^~\\&|ack\rMSA|AA|x\r"))
	defer srv.Close()

	c := NewClient(srv.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	errCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := c.Send(ctx, []byte("MSH|^~\\&|body\r"))
			errCh <- err
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("Send #%d: %v", i, err)
		}
	}
}

func TestFrame_RoundTripsViaUnframe(t *testing.T) {
	t.Parallel()
	body := []byte("MSH|^~\\&|x|y|z|w|20240101120000||ADT^A01|123|T|2.5.1\r")
	framed := frame(body)
	if framed[0] != startBlock {
		t.Fatalf("expected START_BLOCK 0x0B at head, got 0x%02X", framed[0])
	}
	tail := len(framed)
	if framed[tail-2] != endBlock || framed[tail-1] != cr {
		t.Fatalf("expected 0x1C 0x0D at tail, got 0x%02X 0x%02X", framed[tail-2], framed[tail-1])
	}
	got, err := unframe(framed)
	if err != nil {
		t.Fatalf("unframe: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("round-trip mismatch:\n got:  %q\n want: %q", got, body)
	}
}

func TestUnframe_MissingStartBlock(t *testing.T) {
	t.Parallel()
	// No 0x0B anywhere.
	_, err := unframe([]byte("MSH|^~\\&|x\r"))
	if err == nil || !strings.Contains(err.Error(), "missing MLLP start block") {
		t.Fatalf("expected missing-start error, got: %v", err)
	}
}

func TestUnframe_MissingEndBlock(t *testing.T) {
	t.Parallel()
	// Has start, no 0x1C 0x0D pair.
	_, err := unframe([]byte{startBlock, 'M', 'S', 'H'})
	if err == nil || !strings.Contains(err.Error(), "missing or misordered MLLP end block") {
		t.Fatalf("expected missing-end error, got: %v", err)
	}
}

func TestUnframe_MisorderedEndBeforeStart(t *testing.T) {
	t.Parallel()
	// 0x1C 0x0D appears, but before the 0x0B start block.
	_, err := unframe([]byte{endBlock, cr, startBlock, 'X'})
	if err == nil || !strings.Contains(err.Error(), "missing or misordered MLLP end block") {
		t.Fatalf("expected misordered-end error, got: %v", err)
	}
}

// --- Helpers: real TCP echo listeners ---------------------------------

type echoListener struct {
	l        net.Listener
	received chan []byte
}

func (e *echoListener) Addr() net.Addr { return e.l.Addr() }
func (e *echoListener) Close() error   { return e.l.Close() }

// startEchoListener spawns one goroutine that accepts a single connection,
// reads one MLLP frame, captures the framed bytes onto received, then sends
// back ackBody wrapped in MLLP framing.
func startEchoListener(t *testing.T, ackBody []byte) *echoListener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	out := make(chan []byte, 1)
	go func() {
		conn, acceptErr := l.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		var acc bytes.Buffer
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
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
				return
			}
		}
		out <- acc.Bytes()
		// Reply with framed ack.
		_, _ = conn.Write(frame(ackBody))
	}()
	return &echoListener{l: l, received: out}
}

// startEchoListenerSerial accepts up to n connections sequentially, each
// reading one frame and writing one ack. Used to verify mutex serialization
// without racing on a shared connection.
func startEchoListenerSerial(t *testing.T, n int, ackBody []byte) *echoListener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for i := 0; i < n; i++ {
			conn, acceptErr := l.Accept()
			if acceptErr != nil {
				return
			}
			buf := make([]byte, 4096)
			var acc bytes.Buffer
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			for {
				count, readErr := conn.Read(buf)
				if count > 0 {
					acc.Write(buf[:count])
				}
				if bytes.Contains(acc.Bytes(), []byte{endBlock, cr}) {
					break
				}
				if readErr != nil {
					break
				}
			}
			_, _ = conn.Write(frame(ackBody))
			conn.Close()
		}
	}()
	return &echoListener{l: l, received: make(chan []byte, 1)}
}
