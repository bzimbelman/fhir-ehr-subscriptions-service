// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// TestProperty_Framer_NeverPanics feeds the framer arbitrary byte streams
// in arbitrary chunk sizes and asserts:
//   - the framer never panics,
//   - every event is one of the three known kinds,
//   - bodies returned by FrameEvent never exceed maxBody,
//   - on Malformed, the framer continues to behave (subsequent calls return
//     a typed event, not a panic).
func TestProperty_Framer_NeverPanics(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		maxBody := rapid.IntRange(1, 4096).Draw(rt, "maxBody")
		input := rapid.SliceOfN(rapid.Byte(), 0, 8192).Draw(rt, "input")
		// Random feed chunks: at least 1, up to len(input).
		var chunks [][]byte
		i := 0
		for i < len(input) {
			step := rapid.IntRange(1, max(1, len(input)-i)).Draw(rt, "step")
			if i+step > len(input) {
				step = len(input) - i
			}
			chunks = append(chunks, input[i:i+step])
			i += step
		}

		f := NewFramer(maxBody)
		// Feed all chunks, draining events between feeds.
		for _, ch := range chunks {
			f.Append(ch)
			for {
				ev := f.Next()
				switch e := ev.(type) {
				case FrameEvent:
					if maxBody > 0 && len(e.Body) > maxBody {
						rt.Fatalf("frame body %d exceeds maxBody %d", len(e.Body), maxBody)
					}
				case MalformedEvent:
					// Acceptable. Verify the reason is one of the four
					// LLD §4 enum values.
					switch e.Reason {
					case ReasonOversizedMessage,
						ReasonUnexpectedStartByteMidFrame,
						ReasonEndBeforeStart,
						ReasonStartWithoutEnd:
					default:
						rt.Fatalf("unknown malformed reason %q", e.Reason)
					}
					// Continue: subsequent calls must still not panic.
					goto NextChunk
				case NeedMoreEvent:
					goto NextChunk
				default:
					rt.Fatalf("unknown event type %T", ev)
				}
			}
		NextChunk:
		}
	})
}

// TestProperty_Framer_RoundTripPreservesBody feeds the framer a sequence
// of well-formed frames (in random chunking) and asserts the body bytes
// emitted match the bytes the test placed between markers.
func TestProperty_Framer_RoundTripPreservesBody(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Build a sequence of bodies. Bodies must contain neither 0x0B nor
		// 0x1C (raw 0x1C followed by 0x0D would close the frame). 0x1C
		// followed by anything else is body content; allowing 0x1C without
		// 0x0D after it complicates the property without adding signal.
		bodies := rapid.SliceOfN(
			rapid.SliceOfN(
				rapid.ByteRange(0x20, 0x7E), // printable ASCII for clarity
				1, 64,
			),
			1, 8,
		).Draw(rt, "bodies")

		// Splice bodies into a wire stream.
		var wire bytes.Buffer
		for _, b := range bodies {
			wire.WriteByte(frameStart)
			wire.Write(b)
			wire.WriteByte(frameEnd1)
			wire.WriteByte(frameEnd2)
		}

		// Feed in random chunks.
		raw := wire.Bytes()
		var chunks [][]byte
		i := 0
		for i < len(raw) {
			step := rapid.IntRange(1, max(1, len(raw)-i)).Draw(rt, "step")
			if i+step > len(raw) {
				step = len(raw) - i
			}
			chunks = append(chunks, raw[i:i+step])
			i += step
		}

		f := NewFramer(8192)
		var got [][]byte
		for _, ch := range chunks {
			f.Append(ch)
			for {
				ev := f.Next()
				if _, ok := ev.(NeedMoreEvent); ok {
					break
				}
				if mal, ok := ev.(MalformedEvent); ok {
					rt.Fatalf("unexpected Malformed(%s) on a clean stream", mal.Reason)
				}
				if fr, ok := ev.(FrameEvent); ok {
					got = append(got, fr.Body)
				}
			}
		}
		if len(got) != len(bodies) {
			rt.Fatalf("got %d frames, want %d", len(got), len(bodies))
		}
		for i := range bodies {
			if !bytes.Equal(got[i], bodies[i]) {
				rt.Fatalf("frame %d mismatch: got %q want %q", i, got[i], bodies[i])
			}
		}
	})
}

// TestProperty_MSH_RoundTrip generates random ADT/ORM/ORU MSH-9 root types
// and random MSH-10 control IDs, builds a minimal MSH segment, and asserts
// ExtractMSH recovers the same fields.
func TestProperty_MSH_RoundTrip(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		root := rapid.SampledFrom([]string{"ADT", "ORM", "ORU", "SIU", "MDM"}).Draw(rt, "root")
		event := rapid.SampledFrom([]string{"A01", "A04", "O01", "R01", "S12", "T02"}).Draw(rt, "event")
		ctrlID := rapid.StringMatching(`[A-Z][A-Z0-9-]{1,16}`).Draw(rt, "ctrlID")
		msh := fmt.Sprintf("MSH|^~\\&|SNDR|FAC|RCVR|RFAC|20240101010101||%s^%s|%s|P|2.5\r", root, event, ctrlID)

		got, err := ExtractMSH([]byte(msh))
		if err != nil {
			rt.Fatalf("ExtractMSH: %v", err)
		}
		if got.MessageType != root {
			rt.Fatalf("MessageType = %q, want %q", got.MessageType, root)
		}
		if got.MessageControlID != ctrlID {
			rt.Fatalf("MessageControlID = %q, want %q", got.MessageControlID, ctrlID)
		}
	})
}

// TestProperty_MultipleEndpoints_NoCrossInterleave runs N concurrent
// endpoints, sends per-endpoint streams of frames whose bodies encode
// the endpoint id, and asserts every persisted row's body originates
// from exactly the endpoint it was received on (no cross-stream
// corruption from the framer or the connection-level state).
func TestProperty_MultipleEndpoints_NoCrossInterleave(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		nEndpoints := rapid.IntRange(2, 4).Draw(rt, "nEndpoints")
		perConn := rapid.IntRange(3, 8).Draw(rt, "perConn")

		p := &fakePersister{}
		m := newFakeMetrics()

		eps := make([]EndpointConfig, nEndpoints)
		for i := range eps {
			eps[i] = EndpointConfig{Name: fmt.Sprintf("ep%d", i)}
		}
		cfg := defaultConfig(eps...)

		var wg sync.WaitGroup
		clients := make([]net.Conn, nEndpoints)
		for i := 0; i < nEndpoints; i++ {
			s, c := net.Pipe()
			clients[i] = c
			ep := eps[i]
			wg.Add(1)
			go func() {
				defer wg.Done()
				HandleConnection(context.Background(), s, ep, cfg, p, m, fmt.Sprintf("10.0.%d.1:1", i))
			}()
		}

		for i := 0; i < nEndpoints; i++ {
			i := i
			go func() {
				defer clients[i].Close()
				for j := 0; j < perConn; j++ {
					body := fmt.Sprintf("MSH|^~\\&|EP%d|FAC|||20240101||ADT^A01|EP%d-MSG-%d|P|2.5\r", i, i, j)
					if _, err := clients[i].Write(frameBytes(body)); err != nil {
						return
					}
					// Read ACK to keep the persist pipeline drained.
					_ = readFrameNoFatal(clients[i], 2*time.Second)
				}
			}()
		}

		// Wait until we have nEndpoints*perConn rows or a timeout.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if len(p.Rows()) >= nEndpoints*perConn {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}

		wg.Wait()

		rows := p.Rows()
		if len(rows) != nEndpoints*perConn {
			rt.Fatalf("rows = %d, want %d", len(rows), nEndpoints*perConn)
		}
		// Every row's body must encode the endpoint it was received on.
		for _, r := range rows {
			marker := fmt.Sprintf("|EP%s-", strings.TrimPrefix(r.ListenerEndpoint, "ep"))
			if !strings.Contains(string(r.Body), marker) {
				rt.Fatalf("row endpoint=%q body lacks expected marker %q: %q",
					r.ListenerEndpoint, marker, r.Body)
			}
		}
	})
}

// readFrameNoFatal is a copy of readFrame that returns nil instead of
// failing the test, so property tests can keep going on slow reads.
func readFrameNoFatal(conn net.Conn, timeout time.Duration) []byte {
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil
	}
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	buf := make([]byte, 0, 256)
	one := make([]byte, 1)
	for {
		n, err := conn.Read(one)
		if err != nil {
			return nil
		}
		if n == 0 {
			continue
		}
		buf = append(buf, one[0])
		if len(buf) >= 3 && buf[0] == frameStart && buf[len(buf)-2] == frameEnd1 && buf[len(buf)-1] == frameEnd2 {
			return buf[1 : len(buf)-2]
		}
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
