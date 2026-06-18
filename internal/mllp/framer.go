// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

// MLLP framing constants per the HL7 MLLP transport specification.
const (
	frameStart = 0x0B
	frameEnd1  = 0x1C
	frameEnd2  = 0x0D
)

// MalformedReason categorizes why the framer rejected a stream of bytes.
type MalformedReason string

// MalformedReason values reported by the framer. Names match the LLD's §4
// MalformedReason enum and are used as label values on the malformed metric.
const (
	ReasonOversizedMessage            MalformedReason = "oversized_message"
	ReasonUnexpectedStartByteMidFrame MalformedReason = "unexpected_start_byte_mid_frame"
	// ReasonEndBeforeStart fires when 0x1C 0x0D appears in the byte
	// stream before any 0x0B has been seen. Per LLD §4 this is a peer
	// software bug; the connection task drops the connection.
	ReasonEndBeforeStart MalformedReason = "end_before_start"
	// ReasonStartWithoutEnd is the framer's terminal classification when
	// Finalize is called while a frame body is in flight (i.e. peer
	// disconnected after 0x0B and before 0x1C 0x0D).
	ReasonStartWithoutEnd MalformedReason = "start_without_end"
)

// FramerEvent is the discriminated union returned by Framer.Next.
// Production code switches on the concrete type rather than a tag.
type FramerEvent interface {
	isFramerEvent()
}

// FrameEvent surfaces a complete inter-marker body. Body is owned by the
// caller after this event is returned: the framer does not retain it.
type FrameEvent struct {
	Body []byte
}

func (FrameEvent) isFramerEvent() {}

// MalformedEvent indicates the byte stream cannot be reframed: the caller
// must drop the connection. The reason is reported as a metric label.
type MalformedEvent struct {
	Reason MalformedReason
}

func (MalformedEvent) isFramerEvent() {}

// NeedMoreEvent indicates the framer's accumulator does not yet contain
// a complete frame. The caller must read more bytes and call Next again.
type NeedMoreEvent struct{}

func (NeedMoreEvent) isFramerEvent() {}

// framerState is the framer's internal mode.
type framerState int

const (
	stateClosed framerState = iota // looking for 0x0B; bytes outside a frame are noise.
	stateOpen                      // accumulating an in-flight frame body.
)

// Framer is a stateful, allocation-light MLLP de-framer. It is NOT safe for
// concurrent use; callers ensure each TCP connection owns one framer.
//
// Usage:
//
//	f := NewFramer(maxBytes)
//	for {
//	    f.Append(read)
//	    for {
//	        switch ev := f.Next().(type) {
//	        case FrameEvent: handle(ev.Body)
//	        case MalformedEvent: dropConnection(); return
//	        case NeedMoreEvent: break // outer loop, read again
//	        }
//	    }
//	}
type Framer struct {
	maxBody int
	state   framerState

	// buf accumulates bytes across calls. When state == stateOpen, buf
	// contains exactly the candidate frame body so far (no start byte,
	// no end bytes); when state == stateClosed, buf is empty (any pre-VT
	// noise is consumed before being appended into the frame body).
	buf []byte

	// pending holds bytes received before they have been classified into
	// either "noise to discard" (stateClosed) or "frame body" (stateOpen).
	// Specifically: in stateClosed, pending may hold bytes preceding the
	// next 0x0B; in stateOpen, pending is unused (bytes go straight to buf).
	pending []byte

	// closedScanned tracks how many leading bytes of `pending` have
	// already been swept for the end-pair while we are in stateClosed.
	// Without it, repeated Next calls on a stream that streams junk
	// before the first 0x0B re-scanned from offset 0 every time —
	// O(n^2) on Append+Next interleaving. Reset to 0 whenever pending
	// is rewritten or state transitions (N-1).
	closedScanned int
}

// NewFramer constructs a framer with the given maximum frame body size in
// bytes. A non-positive max disables the size limit and is intended only
// for tests.
func NewFramer(maxBody int) *Framer {
	return &Framer{maxBody: maxBody, state: stateClosed}
}

// Append feeds raw bytes from the wire into the framer. The framer copies
// only what it needs, so callers may reuse the input slice immediately.
func (f *Framer) Append(p []byte) {
	if len(p) == 0 {
		return
	}
	f.pending = append(f.pending, p...)
}

// pendingExceeded reports whether pending has grown past 2× maxBody;
// callers (Next) treat that as a slowloris signal and emit Malformed
// (S-9.4). A non-positive maxBody disables the cap.
func (f *Framer) pendingExceeded() bool {
	return f.maxBody > 0 && len(f.pending) > 2*f.maxBody
}

// AssemblyInProgress reports whether the framer is currently mid-frame —
// that is, the start byte 0x0B has been observed but the frame's end
// pair (0x1C 0x0D) has not yet arrived. The connection-level read loop
// uses this to arm the FrameAssemblyTimeout (S-9.1): the deadline runs
// only while we are actively assembling a frame, not while the
// connection is idle between frames.
func (f *Framer) AssemblyInProgress() bool {
	return f.state == stateOpen || len(f.buf) > 0
}

// Next returns the next event. Callers must keep calling Next until it
// returns a NeedMoreEvent or a MalformedEvent.
func (f *Framer) Next() FramerEvent {
	for {
		// S-9.4: bound pending growth across calls. A peer that
		// streams junk faster than Next can drain it (or that holds
		// a frame mid-body indefinitely) cannot grow pending without
		// limit. We reuse the OversizedMessage classification so
		// existing callers (which drop the connection on Malformed)
		// already handle it.
		if f.pendingExceeded() {
			f.state = stateClosed
			f.buf = f.buf[:0]
			f.pending = f.pending[:0]
			return MalformedEvent{Reason: ReasonOversizedMessage}
		}
		switch f.state {
		case stateClosed:
			// Find 0x0B; bytes before it are noise (per LLD section 5.4),
			// EXCEPT a 0x1C 0x0D pair before the first 0x0B is per LLD §4
			// EndBeforeStart — a peer software bug that triggers a drop.
			startIdx := indexOf(f.pending, frameStart)
			// N-1: incremental end-pair scan. We have already swept
			// pending[:closedScanned] in a prior Next call; only sweep
			// the new tail so repeated junk does not produce O(n^2)
			// scanning. The scan window's upper bound is min(len, startIdx).
			upper := len(f.pending)
			if startIdx >= 0 && startIdx < upper {
				upper = startIdx
			}
			scanFrom := f.closedScanned
			if scanFrom > 0 {
				// Step back one byte so we catch a pair straddling the
				// previous scan boundary.
				scanFrom--
			}
			endBeforeStart := false
			if scanFrom < upper && scanEndPairRange(f.pending, scanFrom, upper) {
				endBeforeStart = true
			}
			f.closedScanned = upper
			if endBeforeStart {
				f.pending = f.pending[:0]
				f.closedScanned = 0
				return MalformedEvent{Reason: ReasonEndBeforeStart}
			}
			if startIdx < 0 {
				// All pending bytes are noise; discard them and ask for more.
				f.pending = f.pending[:0]
				f.closedScanned = 0
				return NeedMoreEvent{}
			}
			// Drop noise + the start byte itself; transition to Open.
			f.pending = f.pending[startIdx+1:]
			f.closedScanned = 0
			f.state = stateOpen
			f.buf = f.buf[:0]
			// Fall through to process any bytes already in pending.

		case stateOpen:
			// Scan pending for a 0x0B (malformed) or 0x1C 0x0D (frame end).
			i := 0
			for i < len(f.pending) {
				b := f.pending[i]
				if b == frameStart {
					// Reset state so a subsequent Next returns NeedMoreEvent
					// from a clean Closed accumulator if the caller chooses
					// to keep reading anyway. We discard pending; the caller
					// is expected to drop the connection.
					f.state = stateClosed
					f.buf = f.buf[:0]
					f.pending = f.pending[:0]
					return MalformedEvent{Reason: ReasonUnexpectedStartByteMidFrame}
				}
				if b == frameEnd1 {
					// Need to peek the next byte. If we don't have it yet,
					// keep these bytes in pending and ask for more.
					if i+1 >= len(f.pending) {
						break
					}
					if f.pending[i+1] == frameEnd2 {
						// Bytes [0..i) belong to the body. Append them and emit.
						if !f.appendBody(f.pending[:i]) {
							// Append rejected for size; reset and report.
							f.state = stateClosed
							f.buf = f.buf[:0]
							f.pending = f.pending[:0]
							return MalformedEvent{Reason: ReasonOversizedMessage}
						}
						body := append([]byte(nil), f.buf...)
						f.buf = f.buf[:0]
						f.state = stateClosed
						// Drop the body bytes plus the two end bytes.
						f.pending = f.pending[i+2:]
						return FrameEvent{Body: body}
					}
					// 0x1C followed by something other than 0x0D is treated
					// as ordinary body content (not malformed). Continue scanning.
				}
				i++
			}
			// We consumed up to len(pending) without finding the end. Move
			// the scanned bytes into buf (they are body so far) and ask for more.
			if i > 0 {
				if !f.appendBody(f.pending[:i]) {
					f.state = stateClosed
					f.buf = f.buf[:0]
					f.pending = f.pending[:0]
					return MalformedEvent{Reason: ReasonOversizedMessage}
				}
				f.pending = f.pending[i:]
			}
			return NeedMoreEvent{}
		}
	}
}

// appendBody appends src to the framer's in-flight body buffer, enforcing
// the maxBody cap. Returns false if appending would exceed maxBody.
func (f *Framer) appendBody(src []byte) bool {
	if f.maxBody > 0 && len(f.buf)+len(src) > f.maxBody {
		return false
	}
	f.buf = append(f.buf, src...)
	return true
}

// indexOf returns the position of the first occurrence of c in b, or -1.
// Inlined to avoid the bytes.IndexByte test-overhead in hot loops; the
// stdlib helper is a fine alternative if the compiler stops inlining this.
func indexOf(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}

// scanEndPairRange reports whether b[from:to] contains the 0x1C 0x0D
// end-byte pair anywhere. The window is half-open. (N-1)
//
// Replaced the prior `scanEndPair(b, startIdx)` whole-prefix scan; the
// windowed form is what production callers use to honor the
// `closedScanned` offset.
func scanEndPairRange(b []byte, from, to int) bool {
	if from < 0 {
		from = 0
	}
	if to > len(b) {
		to = len(b)
	}
	for i := from; i+1 < to; i++ {
		if b[i] == frameEnd1 && b[i+1] == frameEnd2 {
			return true
		}
	}
	return false
}

// Finalize is the framer's terminal classification surface. The caller
// invokes it once on peer disconnect (or any other read-loop exit) so the
// framer can report whether an in-flight frame was abandoned. Returns:
//
//   - NeedMoreEvent{}   — framer is closed; nothing to clean up.
//   - MalformedEvent{Reason: ReasonStartWithoutEnd} — body in flight, no
//     end bytes; peer disconnected mid-frame.
//
// Subsequent calls return NeedMore (Finalize is idempotent).
func (f *Framer) Finalize() FramerEvent {
	if f.state == stateOpen || len(f.buf) > 0 {
		f.state = stateClosed
		f.buf = f.buf[:0]
		f.pending = f.pending[:0]
		return MalformedEvent{Reason: ReasonStartWithoutEnd}
	}
	return NeedMoreEvent{}
}
