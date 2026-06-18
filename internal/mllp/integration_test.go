// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package mllp

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// TestIntegration_RealTCP_RoundTrip stands up the listener on 127.0.0.1:0
// (kernel-assigned port), connects a real TCP client, sends a framed HL7
// message, asserts the AA ACK comes back, and confirms the persister
// captured the row.
func TestIntegration_RealTCP_RoundTrip(t *testing.T) {
	p := &fakePersister{}
	m := newFakeMetrics()

	cfg := ListenerConfig{
		Endpoints: []EndpointConfig{
			{Name: "adt-feed", Bind: "127.0.0.1:0"},
		},
		MaxMessageBytes:    1 << 20,
		ReadIdleTimeout:    5 * time.Second,
		PersistTimeout:     2 * time.Second,
		NackThenDropAfter:  3,
		ShutdownDrainGrace: 2 * time.Second,
		InflightCapPerConn: 8,
	}

	l := New(cfg, p, m, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := l.Start(ctx); err != nil {
		t.Fatalf("listener start: %v", err)
	}
	defer func() {
		_ = l.Shutdown(context.Background())
	}()

	addr := l.Addr("adt-feed")
	if addr == nil {
		t.Fatalf("listener did not assign an address")
	}

	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	body := "MSH|^~\\&|SNDR|FAC|RCVR|RFAC|20240101010101||ORU^R01|INT-MSG-1|P|2.5\rPID|||x\r"
	if _, err := conn.Write(frameBytes(body)); err != nil {
		t.Fatalf("write framed message: %v", err)
	}

	ack := readFullFrame(t, conn, 3*time.Second)
	if !strings.Contains(string(ack), "MSA|AA|INT-MSG-1") {
		t.Fatalf("expected AA ACK echoing INT-MSG-1; got %q", ack)
	}

	// The persister write happens before the ACK; the row must be present.
	rows := p.Rows()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].MLLPMessageID != "INT-MSG-1" {
		t.Fatalf("row.MLLPMessageID = %q", rows[0].MLLPMessageID)
	}
	if string(rows[0].Body) != body {
		t.Fatalf("row.Body mismatch")
	}
	if rows[0].PeerAddr == "" {
		t.Fatalf("row.PeerAddr should be set")
	}
}

// TestIntegration_RealTCP_MultipleEndpointsConcurrent runs two real TCP
// endpoints, sends many messages through both concurrently, asserts every
// row is persisted exactly once and tagged with its source endpoint.
func TestIntegration_RealTCP_MultipleEndpointsConcurrent(t *testing.T) {
	p := &fakePersister{}
	m := newFakeMetrics()

	cfg := ListenerConfig{
		Endpoints: []EndpointConfig{
			{Name: "ep-a", Bind: "127.0.0.1:0"},
			{Name: "ep-b", Bind: "127.0.0.1:0"},
		},
		MaxMessageBytes:    1 << 20,
		ReadIdleTimeout:    5 * time.Second,
		PersistTimeout:     2 * time.Second,
		NackThenDropAfter:  5,
		ShutdownDrainGrace: 2 * time.Second,
		InflightCapPerConn: 8,
	}

	l := New(cfg, p, m, nil)
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("listener start: %v", err)
	}
	defer func() { _ = l.Shutdown(context.Background()) }()

	type clientResult struct {
		ep      string
		count   int
		err     error
	}
	results := make(chan clientResult, 2)

	const messagesPerClient = 20
	for _, name := range []string{"ep-a", "ep-b"} {
		name := name
		addr := l.Addr(name).String()
		go func() {
			conn, err := net.Dial("tcp", addr)
			if err != nil {
				results <- clientResult{ep: name, err: err}
				return
			}
			defer conn.Close()
			for j := 0; j < messagesPerClient; j++ {
				body := fmt.Sprintf("MSH|^~\\&|SNDR|FAC|||20240101||ADT^A01|%s-MSG-%d|P|2.5\r", name, j)
				if _, err := conn.Write(frameBytes(body)); err != nil {
					results <- clientResult{ep: name, count: j, err: err}
					return
				}
				ack := readFullFrame(t, conn, 3*time.Second)
				if !bytes.Contains(ack, []byte(fmt.Sprintf("MSA|AA|%s-MSG-%d", name, j))) {
					results <- clientResult{ep: name, count: j, err: fmt.Errorf("ack mismatch %q", ack)}
					return
				}
			}
			results <- clientResult{ep: name, count: messagesPerClient}
		}()
	}

	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Fatalf("client %s after %d messages: %v", r.ep, r.count, r.err)
		}
		if r.count != messagesPerClient {
			t.Fatalf("client %s sent %d, want %d", r.ep, r.count, messagesPerClient)
		}
	}

	// Allow brief moment for any final post-ACK bookkeeping; the persister
	// is incremented before the ACK so there is no real race here, but
	// staying explicit keeps the test robust under heavy load.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(p.Rows()) < 2*messagesPerClient {
		time.Sleep(10 * time.Millisecond)
	}

	rows := p.Rows()
	if len(rows) != 2*messagesPerClient {
		t.Fatalf("rows = %d, want %d", len(rows), 2*messagesPerClient)
	}
	byEP := map[string]int{}
	for _, r := range rows {
		byEP[r.ListenerEndpoint]++
	}
	for _, ep := range []string{"ep-a", "ep-b"} {
		if byEP[ep] != messagesPerClient {
			t.Fatalf("rows for %s = %d, want %d", ep, byEP[ep], messagesPerClient)
		}
	}
}

// readFullFrame reads from conn until the MLLP end markers appear.
func readFullFrame(t *testing.T, conn net.Conn, timeout time.Duration) []byte {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	buf := make([]byte, 0, 256)
	one := make([]byte, 1)
	for {
		n, err := conn.Read(one)
		if err != nil {
			t.Fatalf("read: %v (so far %q)", err, buf)
		}
		if n == 0 {
			continue
		}
		buf = append(buf, one[0])
		if len(buf) >= 3 && buf[0] == frameStart && buf[len(buf)-2] == frameEnd1 && buf[len(buf)-1] == frameEnd2 {
			return buf
		}
	}
}
