// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// SHOULD-FIX coverage for S-9 audit findings (mllp half).

package mllp

import (
	"errors"
	"net"
	"strings"
	"testing"
)

// TestS9_3_IsClosedConnErrUsesNetErrClosed — S-9.3: substring match on
// "closed" trips on any error message containing the word; switch to
// errors.Is(net.ErrClosed) and only fall back to the pipe sentinel.
func TestS9_3_IsClosedConnErrUsesNetErrClosed(t *testing.T) {
	t.Parallel()

	if !isClosedConnErr(net.ErrClosed) {
		t.Errorf("net.ErrClosed should be classified as closed-conn")
	}
	// Wrapped — must use errors.Is, not substring on wrapper.Error().
	if !isClosedConnErr(wrap(net.ErrClosed)) {
		t.Errorf("wrapped net.ErrClosed should be classified as closed-conn")
	}
	// net.Pipe surfaces "io: read/write on closed pipe"; the LLD
	// supports recognizing that for tests; fine.
	pipeErr := errors.New("io: read/write on closed pipe")
	if !isClosedConnErr(pipeErr) {
		t.Errorf("pipe-closed sentinel should be classified as closed-conn")
	}
	// An error with the word "closed" in unrelated context — vendor
	// strings like "JWKS host closed for maintenance" must NOT be
	// misclassified as a closed connection (S-9.3).
	otherErr := errors.New("JWKS host closed for maintenance")
	if isClosedConnErr(otherErr) {
		t.Errorf("unrelated error containing 'closed' should not be classified as closed-conn")
	}
}

type wrapErr struct{ inner error }

func (w wrapErr) Error() string { return "wrapped: " + w.inner.Error() }
func (w wrapErr) Unwrap() error { return w.inner }
func wrap(err error) error      { return wrapErr{inner: err} }

// TestS9_4_FramerPendingBoundedByMaxBody — S-9.4: pending was unbounded
// across calls; large slowloris-style streams without a 0x0B start byte
// could grow it without limit. Validate the framer rejects pending
// growth past 2× maxBody — post-OP-#227, Append rejects up front via
// ErrPendingExceeded; pre-fix, Next surfaced MalformedEvent{Oversized}.
// Both paths preserve the same connection-level outcome (drop the peer)
// so we accept either signal.
func TestS9_4_FramerPendingBoundedByMaxBody(t *testing.T) {
	t.Parallel()
	const maxBody = 1024
	f := NewFramer(maxBody)
	// Feed 4× the cap of pre-VT noise (no 0x0B). Post-#227 Append
	// returns ErrPendingExceeded eagerly.
	noise := strings.Repeat("X", 4*maxBody)
	if err := f.Append([]byte(noise)); err != nil {
		// Eager rejection path — accepted.
		return
	}
	// Legacy fallback: framer accepted but Next() must surface oversized.
	ev := f.Next()
	mal, ok := ev.(MalformedEvent)
	if !ok {
		t.Fatalf("expected MalformedEvent, got %#v", ev)
	}
	if mal.Reason != ReasonOversizedMessage {
		t.Errorf("malformed reason: got %q want OversizedMessage", mal.Reason)
	}
}

// TestS9_5_ExtractMSHReturnsCharsetField — S-9.5: MSH-18 charset is now
// surfaced so callers can log / metric the encoding (or refuse to
// process non-ASCII content if they choose). Empty when MSH-18 is
// absent.
func TestS9_5_ExtractMSHReturnsCharsetField(t *testing.T) {
	t.Parallel()
	// Layout: MSH | encChars | sender | facility | recv | recF | dt |
	// security | type | ctlid | proc | ver | seq | contflag | accept |
	// app ack | country | charset
	// MSH-1 = "|", MSH-2 = "^~\&", MSH-3..18 below.
	// fields[i] = MSH-(i+1). Charset = MSH-18 = fields[17].
	body := []byte("MSH|^~\\&|SENDER|FAC|RECV|RECF|20240101000000||ORU^R01|MSGID|P|2.5.1||||||UNICODE UTF-8\r")
	got, err := ExtractMSH(body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Charset != "UNICODE UTF-8" {
		t.Errorf("Charset got %q want %q", got.Charset, "UNICODE UTF-8")
	}
	if got.MessageType != "ORU" {
		t.Errorf("MessageType got %q want ORU", got.MessageType)
	}
	// Absent MSH-18 — Charset is empty, no error.
	body2 := []byte("MSH|^~\\&|SENDER|FAC|RECV|RECF|20240101000000||ORU^R01|MSGID|P|2.5.1\r")
	got, err = ExtractMSH(body2)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Charset != "" {
		t.Errorf("Charset got %q want empty", got.Charset)
	}
}

// TestS9_10_ExtractMSHReturnsTimestampField — S-9.10: hl7processor.occurred
// should source from MSH-7 when present. Surface MSH-7 from
// ExtractMSH so the processor can use it.
func TestS9_10_ExtractMSHReturnsTimestampField(t *testing.T) {
	t.Parallel()
	body := []byte("MSH|^~\\&|SENDER|FAC|RECV|RECF|20240107120530||ORU^R01|MSGID|P|2.5.1\r")
	got, err := ExtractMSH(body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.MessageDateTime != "20240107120530" {
		t.Errorf("MessageDateTime got %q want %q", got.MessageDateTime, "20240107120530")
	}
}
