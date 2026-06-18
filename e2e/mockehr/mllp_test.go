// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package mockehr

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// MLLP framing per the HL7 v2 transport spec:
//
//   START_BLOCK  = 0x0B  (\v, "VT")
//   END_BLOCK    = 0x1C  (\x1c, "FS")
//   CARRIAGE_RTN = 0x0D  (\r)
//
// One framed message = START_BLOCK + body + END_BLOCK + CARRIAGE_RTN.
//
// The mock EHR's MLLP server is the *sender* side: it dials a configured
// listener, frames a builder-produced HL7 string, writes the framed bytes,
// and reads the listener's ACK frame back.

func TestMLLPClient_FramesAndReceivesACK(t *testing.T) {
	t.Parallel()
	// Spin up a tiny TCP server that mimics fhir-subs' listener: read one
	// MLLP frame, then write back an ACK^AA frame.
	srv := startEchoMLLPListener(t, ackKindApplicationAccept)
	defer srv.Close()

	client := NewMLLPClient(srv.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	body := BuildADT(ADTOptions{TriggerEvent: "A01", MessageID: "MLLP0001", PatientID: "MRN1"})
	ack, err := client.Send(ctx, []byte(body))
	if err != nil {
		t.Fatalf("client.Send returned error: %v", err)
	}
	if !bytes.Contains(ack, []byte("MSA|AA|MLLP0001")) {
		t.Fatalf("expected MSA|AA|MLLP0001 in ACK, got: %q", ack)
	}
}

func TestMLLPClient_RejectsOversizedFrame(t *testing.T) {
	t.Parallel()
	client := NewMLLPClient("127.0.0.1:0")
	client.MaxMessageBytes = 16
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_, err := client.Send(ctx, []byte("MSH|^~\\&|this is way more than sixteen bytes\r"))
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("expected ErrMessageTooLarge, got: %v", err)
	}
}

func TestFrameBytes_RoundTrips(t *testing.T) {
	t.Parallel()
	body := []byte("MSH|^~\\&|x|y|z|w|20240101120000||ADT^A01|123|T|2.5.1\r")
	framed := frameMLLP(body)
	if framed[0] != 0x0B {
		t.Fatalf("expected START_BLOCK 0x0B, got 0x%02X", framed[0])
	}
	tailLen := len(framed)
	if framed[tailLen-2] != 0x1C || framed[tailLen-1] != 0x0D {
		t.Fatalf("expected END_BLOCK+CR at tail, got 0x%02X 0x%02X",
			framed[tailLen-2], framed[tailLen-1])
	}

	got, err := unframeMLLP(framed)
	if err != nil {
		t.Fatalf("unframeMLLP error: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("round-trip mismatch:\n want: %q\n got:  %q", body, got)
	}
}

// --- Local helpers (test-only): an echo MLLP listener mock --------------

type echoListener struct {
	l net.Listener
}

func (e *echoListener) Addr() net.Addr { return e.l.Addr() }
func (e *echoListener) Close() error   { return e.l.Close() }

// startEchoMLLPListener accepts one connection, reads exactly one MLLP
// frame, and writes a single ACK^AA back. Used to verify the client's
// framing.
func startEchoMLLPListener(t *testing.T, kind ackKind) *echoListener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		conn, acceptErr := l.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		var acc bytes.Buffer
		for {
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			n, readErr := conn.Read(buf)
			if n > 0 {
				acc.Write(buf[:n])
			}
			if bytes.Contains(acc.Bytes(), []byte{0x1C, 0x0D}) {
				break
			}
			if readErr != nil {
				if readErr == io.EOF {
					break
				}
				return
			}
		}
		body, err := unframeMLLP(acc.Bytes())
		if err != nil {
			return
		}
		ctrl := extractMSH10(body)
		ack := buildACK(kind, ctrl)
		_, _ = conn.Write(frameMLLP([]byte(ack)))
	}()
	return &echoListener{l: l}
}

func TestExtractMSH10(t *testing.T) {
	t.Parallel()
	body := BuildADT(ADTOptions{TriggerEvent: "A01", MessageID: "X9000", PatientID: "P"})
	got := extractMSH10([]byte(body))
	if got != "X9000" {
		t.Fatalf("extractMSH10: got %q want %q", got, "X9000")
	}
}

func TestBuildACK_HasMSAField(t *testing.T) {
	t.Parallel()
	got := buildACK(ackKindApplicationAccept, "ABC")
	if !bytes.Contains([]byte(got), []byte("MSA|AA|ABC")) {
		t.Fatalf("ACK missing MSA|AA|ABC, got: %s", got)
	}
}
