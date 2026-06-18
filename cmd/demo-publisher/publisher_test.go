// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/cliprint"
)

// TestPublishCatalog_SendsAllMessagesAndPrintsACKs runs the end-to-end happy
// path: a local MLLP echo listener, a 2-message catalog, and verifies that
// (a) both messages reach the listener, (b) MSH-9 and patient_id round-trip,
// and (c) the publisher prints a "→" send line and "←" ACK line per message.
func TestPublishCatalog_SendsAllMessagesAndPrintsACKs(t *testing.T) {
	t.Parallel()
	srv := startTestMLLPListener(t)
	defer srv.Close()

	cat := &Catalog{
		Messages: []MessageEntry{
			{
				Description: "Lab result for ABC123",
				Delay:       0, // no sleep in tests
				Template:    "oru-r01",
				Fields: map[string]string{
					"patient_id":       "ABC123",
					"observation_code": "718-7",
					"value":            "13.5",
					"unit":             "g/dL",
				},
			},
			{
				Description: "Encounter admit for ABC123",
				Delay:       0,
				Template:    "adt-a01",
				Fields: map[string]string{
					"patient_id": "ABC123",
				},
			},
		},
	}

	out := &bytes.Buffer{}
	pub := &publisher{
		addr:  srv.Addr().String(),
		fmt:   cliprint.NewFormatter(out, cliprint.Options{Pretty: true, NoColor: true}),
		nowFn: func() time.Time { return time.Date(2026, 1, 2, 14, 1, 2, 0, time.UTC) },
		idFn:  sequentialIDs("DEMO"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pub.run(ctx, cat); err != nil {
		t.Fatalf("publisher.run: %v", err)
	}

	got := srv.Received()
	if len(got) != 2 {
		t.Fatalf("expected listener to receive 2 messages, got %d", len(got))
	}
	if !bytes.Contains(got[0], []byte("ORU^R01")) {
		t.Fatalf("first message MSH-9: got %q", got[0])
	}
	if !bytes.Contains(got[0], []byte("ABC123")) {
		t.Fatalf("first message missing patient: %q", got[0])
	}
	if !bytes.Contains(got[1], []byte("ADT^A01")) {
		t.Fatalf("second message MSH-9: got %q", got[1])
	}

	output := out.String()
	if !strings.Contains(output, "→") {
		t.Fatalf("expected '→' send arrow in output; got:\n%s", output)
	}
	if !strings.Contains(output, "←") {
		t.Fatalf("expected '←' ACK arrow in output; got:\n%s", output)
	}
	if !strings.Contains(output, "ORU^R01") {
		t.Fatalf("expected ORU^R01 in send line; got:\n%s", output)
	}
	if !strings.Contains(output, "ADT^A01") {
		t.Fatalf("expected ADT^A01 in send line; got:\n%s", output)
	}
	// Each message produces one "→" send and one "←" ACK line.
	if got := strings.Count(output, "→"); got != 2 {
		t.Fatalf("expected 2 send arrows, got %d in:\n%s", got, output)
	}
	if got := strings.Count(output, "←"); got != 2 {
		t.Fatalf("expected 2 ack arrows, got %d in:\n%s", got, output)
	}
}

// TestPublishCatalog_JSONLinesMode runs the publisher with Pretty=false
// and asserts each emitted line is parseable JSON with the expected
// kind/label fields. The send + ack pair per message yields two JSON
// records.
func TestPublishCatalog_JSONLinesMode(t *testing.T) {
	t.Parallel()
	srv := startTestMLLPListener(t)
	defer srv.Close()

	cat := &Catalog{
		Messages: []MessageEntry{{
			Description: "Lab result for ABC123",
			Template:    "oru-r01",
			Fields: map[string]string{
				"patient_id":       "ABC123",
				"observation_code": "718-7",
				"value":            "13.5",
			},
		}},
	}

	out := &bytes.Buffer{}
	pub := &publisher{
		addr:  srv.Addr().String(),
		fmt:   cliprint.NewFormatter(out, cliprint.Options{Pretty: false}),
		nowFn: func() time.Time { return time.Date(2026, 1, 2, 14, 1, 2, 0, time.UTC) },
		idFn:  sequentialIDs("DEMO"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pub.run(ctx, cat); err != nil {
		t.Fatalf("publisher.run: %v", err)
	}

	output := out.String()
	if strings.Contains(output, "\x1b[") {
		t.Errorf("JSON mode must not emit ANSI: %q", output)
	}
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 JSON lines (send + ack), got %d:\n%s", len(lines), output)
	}
	var send, ack map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &send); err != nil {
		t.Fatalf("send line not JSON: %v\n%s", err, lines[0])
	}
	if err := json.Unmarshal([]byte(lines[1]), &ack); err != nil {
		t.Fatalf("ack line not JSON: %v\n%s", err, lines[1])
	}
	if send["kind"] != "send" || send["label"] != "ORU^R01" {
		t.Errorf("send record wrong: %+v", send)
	}
	if ack["kind"] != "ack" || ack["label"] != "ACK" {
		t.Errorf("ack record wrong: %+v", ack)
	}
}

func TestPublishCatalog_StopsOnContextCancel(t *testing.T) {
	t.Parallel()
	srv := startTestMLLPListener(t)
	defer srv.Close()

	cat := &Catalog{
		Messages: []MessageEntry{
			{Delay: 50 * time.Millisecond, Template: "adt-a01", Fields: map[string]string{"patient_id": "P"}},
			{Delay: 50 * time.Millisecond, Template: "adt-a01", Fields: map[string]string{"patient_id": "P"}},
			{Delay: 50 * time.Millisecond, Template: "adt-a01", Fields: map[string]string{"patient_id": "P"}},
			{Delay: 50 * time.Millisecond, Template: "adt-a01", Fields: map[string]string{"patient_id": "P"}},
		},
	}
	pub := &publisher{
		addr:  srv.Addr().String(),
		fmt:   cliprint.NewFormatter(io.Discard, cliprint.Options{Pretty: true, NoColor: true}),
		nowFn: time.Now,
		idFn:  sequentialIDs("CXL"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	err := pub.run(ctx, cat)
	if err == nil {
		t.Fatal("expected context cancel error from publisher.run")
	}
	if !strings.Contains(err.Error(), "context") {
		t.Fatalf("expected ctx-related err, got: %v", err)
	}
}

// --- MLLP test listener -------------------------------------------------

type testMLLPListener struct {
	l        net.Listener
	mu       sync.Mutex
	received [][]byte
	wg       sync.WaitGroup
}

const (
	startBlock = 0x0B
	endBlock   = 0x1C
	cr         = 0x0D
)

func (s *testMLLPListener) Addr() net.Addr { return s.l.Addr() }
func (s *testMLLPListener) Close() error {
	err := s.l.Close()
	s.wg.Wait()
	return err
}
func (s *testMLLPListener) Received() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.received))
	for i, b := range s.received {
		out[i] = append([]byte(nil), b...)
	}
	return out
}

func startTestMLLPListener(t *testing.T) *testMLLPListener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &testMLLPListener{l: l}
	go func() {
		for {
			conn, acceptErr := l.Accept()
			if acceptErr != nil {
				return
			}
			srv.wg.Add(1)
			go func(conn net.Conn) {
				defer srv.wg.Done()
				defer conn.Close()
				_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
				body, readErr := readOneFrame(conn)
				if readErr != nil {
					return
				}
				srv.mu.Lock()
				srv.received = append(srv.received, body)
				srv.mu.Unlock()
				ctrl := extractCtrlIDForTest(body)
				ack := buildAATestACK(ctrl)
				_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
				_, _ = conn.Write(frameForTest([]byte(ack)))
			}(conn)
		}
	}()
	return srv
}

func readOneFrame(conn net.Conn) ([]byte, error) {
	buf := make([]byte, 4096)
	var acc bytes.Buffer
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			acc.Write(buf[:n])
			if bytes.Contains(acc.Bytes(), []byte{endBlock, cr}) {
				return unframeForTest(acc.Bytes())
			}
		}
		if err != nil {
			return nil, err
		}
	}
}

func frameForTest(body []byte) []byte {
	out := make([]byte, 0, len(body)+3)
	out = append(out, startBlock)
	out = append(out, body...)
	out = append(out, endBlock, cr)
	return out
}

func unframeForTest(framed []byte) ([]byte, error) {
	si := bytes.IndexByte(framed, startBlock)
	if si < 0 {
		return nil, fmt.Errorf("no start block")
	}
	ei := bytes.LastIndex(framed, []byte{endBlock, cr})
	if ei <= si {
		return nil, fmt.Errorf("no end block")
	}
	return append([]byte(nil), framed[si+1:ei]...), nil
}

func extractCtrlIDForTest(body []byte) string {
	end := bytes.IndexByte(body, '\r')
	if end < 0 {
		end = len(body)
	}
	parts := strings.Split(string(body[:end]), "|")
	if len(parts) <= 9 {
		return ""
	}
	return parts[9]
}

func buildAATestACK(ctrlID string) string {
	now := time.Now().UTC().Format("20060102150405")
	msh := "MSH|^~\\&|FHIRSUBS|TEST|MOCKEHR|E2E|" + now + "||ACK|ACK-" + ctrlID + "|T|2.5.1\r"
	msa := "MSA|AA|" + ctrlID + "\r"
	return msh + msa
}

func sequentialIDs(prefix string) func() string {
	var n int
	var mu sync.Mutex
	return func() string {
		mu.Lock()
		n++
		id := fmt.Sprintf("%s%04d", prefix, n)
		mu.Unlock()
		return id
	}
}
