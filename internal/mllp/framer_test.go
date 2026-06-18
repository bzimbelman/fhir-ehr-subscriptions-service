// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

import (
	"bytes"
	"testing"
)

const (
	startByte = 0x0B
	endByte1  = 0x1C
	endByte2  = 0x0D
)

// framerOutcome is the test-side projection of the framer's discriminated union.
// We assert against this rather than coupling tests to the production type names.
type framerOutcome struct {
	kind   string // "frame", "need_more", "malformed"
	body   []byte
	reason MalformedReason
}

// drainFramer feeds the framer everything in `input` (in one shot) and then
// keeps calling Next until it returns NeedMore or a Malformed event. It returns
// the ordered list of outcomes.
func drainFramer(t *testing.T, f *Framer, input []byte) []framerOutcome {
	t.Helper()
	if len(input) > 0 {
		f.Append(input)
	}
	out := []framerOutcome{}
	for {
		ev := f.Next()
		switch e := ev.(type) {
		case FrameEvent:
			b := append([]byte(nil), e.Body...)
			out = append(out, framerOutcome{kind: "frame", body: b})
		case MalformedEvent:
			out = append(out, framerOutcome{kind: "malformed", reason: e.Reason})
			return out
		case NeedMoreEvent:
			out = append(out, framerOutcome{kind: "need_more"})
			return out
		default:
			t.Fatalf("unknown framer event type %T", ev)
		}
	}
}

func makeFrame(body []byte) []byte {
	out := make([]byte, 0, len(body)+3)
	out = append(out, startByte)
	out = append(out, body...)
	out = append(out, endByte1, endByte2)
	return out
}

func TestFramer_CompleteFrame(t *testing.T) {
	f := NewFramer(1024)
	body := []byte("MSH|^~\\&|FOO|BAR|||20240101||ADT^A01|MSG-1|P|2.5\r")
	got := drainFramer(t, f, makeFrame(body))
	if len(got) < 2 {
		t.Fatalf("want at least 2 events (frame + need_more), got %d: %#v", len(got), got)
	}
	if got[0].kind != "frame" || !bytes.Equal(got[0].body, body) {
		t.Fatalf("first event must be frame with verbatim body; got kind=%q body=%q", got[0].kind, got[0].body)
	}
	if got[1].kind != "need_more" {
		t.Fatalf("after a complete frame the framer must report need_more; got %q", got[1].kind)
	}
}

func TestFramer_FrameSplitAcrossReads(t *testing.T) {
	f := NewFramer(1024)
	body := []byte("MSH|^~\\&|A|B|||20240101||ADT^A01|MSG-2|P|2.5\r")
	wire := makeFrame(body)

	// Split somewhere in the middle.
	mid := len(wire) / 2
	first := drainFramer(t, f, wire[:mid])
	if len(first) != 1 || first[0].kind != "need_more" {
		t.Fatalf("partial input must produce need_more only; got %#v", first)
	}
	second := drainFramer(t, f, wire[mid:])
	if len(second) < 1 || second[0].kind != "frame" || !bytes.Equal(second[0].body, body) {
		t.Fatalf("after second read framer must surface frame with body; got %#v", second)
	}
}

func TestFramer_EndBytesSplitAcrossReads(t *testing.T) {
	f := NewFramer(1024)
	body := []byte("MSH|^~\\&|A|B|||20240101||ADT^A01|MSG-3|P|2.5\r")
	wire := makeFrame(body)

	// Split between 0x1C and 0x0D specifically.
	splitAt := len(wire) - 1 // last byte is 0x0D
	first := drainFramer(t, f, wire[:splitAt])
	if len(first) != 1 || first[0].kind != "need_more" {
		t.Fatalf("waiting on 0x0D must yield need_more; got %#v", first)
	}
	second := drainFramer(t, f, wire[splitAt:])
	if len(second) < 1 || second[0].kind != "frame" || !bytes.Equal(second[0].body, body) {
		t.Fatalf("end-byte split should still detect frame; got %#v", second)
	}
}

func TestFramer_TwoFramesInOneRead(t *testing.T) {
	f := NewFramer(1024)
	a := []byte("MSH|^~\\&|A|B|||20240101||ADT^A01|MSG-A|P|2.5\r")
	b := []byte("MSH|^~\\&|A|B|||20240101||ADT^A01|MSG-B|P|2.5\r")
	wire := append(makeFrame(a), makeFrame(b)...)
	got := drainFramer(t, f, wire)
	if len(got) < 3 {
		t.Fatalf("want frame+frame+need_more (3 events), got %d: %#v", len(got), got)
	}
	if got[0].kind != "frame" || !bytes.Equal(got[0].body, a) {
		t.Fatalf("first frame body mismatch: %q", got[0].body)
	}
	if got[1].kind != "frame" || !bytes.Equal(got[1].body, b) {
		t.Fatalf("second frame body mismatch: %q", got[1].body)
	}
	if got[2].kind != "need_more" {
		t.Fatalf("after both frames want need_more; got %q", got[2].kind)
	}
}

func TestFramer_OversizeMessage(t *testing.T) {
	f := NewFramer(8) // tiny limit forces overflow
	body := bytes.Repeat([]byte("A"), 16)
	got := drainFramer(t, f, makeFrame(body))
	// The framer must surface Malformed(OversizedMessage) before returning a frame.
	if len(got) == 0 {
		t.Fatalf("expected at least one event")
	}
	last := got[len(got)-1]
	if last.kind != "malformed" {
		t.Fatalf("oversize must report malformed; got %#v", got)
	}
	if last.reason != ReasonOversizedMessage {
		t.Fatalf("oversize reason = %q, want OversizedMessage", last.reason)
	}
}

func TestFramer_MissingStartByte_DiscardsNoise(t *testing.T) {
	f := NewFramer(1024)
	// Junk before the start byte should be discarded.
	body := []byte("MSH|^~\\&|A|B|||20240101||ADT^A01|MSG-N|P|2.5\r")
	wire := append([]byte("garbage-bytes-before-vt"), makeFrame(body)...)
	got := drainFramer(t, f, wire)
	if len(got) < 1 || got[0].kind != "frame" || !bytes.Equal(got[0].body, body) {
		t.Fatalf("noise before start byte must be discarded and the frame surfaced; got %#v", got)
	}
}

func TestFramer_MissingEndBytes_NeedsMore(t *testing.T) {
	f := NewFramer(1024)
	// Complete start + body but no end bytes -> need_more.
	body := []byte("MSH|^~\\&|A|B|||20240101||ADT^A01|MSG-M|P|2.5\r")
	wire := append([]byte{startByte}, body...)
	got := drainFramer(t, f, wire)
	if len(got) != 1 || got[0].kind != "need_more" {
		t.Fatalf("missing end bytes must yield only need_more; got %#v", got)
	}
}

func TestFramer_StartByteMidFrame_Malformed(t *testing.T) {
	f := NewFramer(1024)
	// 0x0B inside an open frame is malformed framing.
	wire := []byte{startByte, 'M', 'S', 'H', '|', startByte, 'X', endByte1, endByte2}
	got := drainFramer(t, f, wire)
	if len(got) == 0 {
		t.Fatalf("expected at least one event")
	}
	last := got[len(got)-1]
	if last.kind != "malformed" {
		t.Fatalf("start-byte mid-frame must be malformed; got %#v", got)
	}
	if last.reason != ReasonUnexpectedStartByteMidFrame {
		t.Fatalf("reason = %q, want UnexpectedStartByteMidFrame", last.reason)
	}
}

func TestFramer_ByteByByte(t *testing.T) {
	f := NewFramer(1024)
	body := []byte("MSH|^~\\&|A|B|||20240101||ADT^A01|MSG-X|P|2.5\r")
	wire := makeFrame(body)
	var firstFrame []byte
	for i := 0; i < len(wire); i++ {
		f.Append(wire[i : i+1])
		for {
			ev := f.Next()
			switch e := ev.(type) {
			case NeedMoreEvent:
				goto NextByte
			case FrameEvent:
				firstFrame = append([]byte(nil), e.Body...)
				goto Done
			case MalformedEvent:
				t.Fatalf("byte-by-byte feed should not be malformed; reason=%q at byte %d", e.Reason, i)
			}
		}
	NextByte:
	}
Done:
	if !bytes.Equal(firstFrame, body) {
		t.Fatalf("byte-by-byte assembled frame body mismatch: got %q want %q", firstFrame, body)
	}
}
