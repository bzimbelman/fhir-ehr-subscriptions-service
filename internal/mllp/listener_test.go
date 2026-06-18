// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakePersister captures rows for assertion and supports per-row hooks
// so tests can simulate transient failures, permanent failures, and
// blocking persistence (for shutdown drain coverage).
type fakePersister struct {
	mu          sync.Mutex
	rows        []QueueRow
	persistedAt []time.Time
	err         error                                            // returned on every call when set
	beforeHook  func(ctx context.Context, row QueueRow) error    // optional pre-write hook
	afterHook   func(ctx context.Context, row QueueRow)          // optional post-write hook
	delay       time.Duration                                    // sleep before returning
	persistFn   func(ctx context.Context, row QueueRow) error    // optional override
}

func (p *fakePersister) Persist(ctx context.Context, row QueueRow) error {
	if p.persistFn != nil {
		return p.persistFn(ctx, row)
	}
	if p.beforeHook != nil {
		if err := p.beforeHook(ctx, row); err != nil {
			return err
		}
	}
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if p.err != nil {
		return p.err
	}
	p.mu.Lock()
	p.rows = append(p.rows, row)
	p.persistedAt = append(p.persistedAt, time.Now())
	p.mu.Unlock()
	if p.afterHook != nil {
		p.afterHook(ctx, row)
	}
	return nil
}

func (p *fakePersister) Rows() []QueueRow {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]QueueRow, len(p.rows))
	copy(out, p.rows)
	return out
}

func (p *fakePersister) PersistedAt() []time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]time.Time, len(p.persistedAt))
	copy(out, p.persistedAt)
	return out
}

// fakeMetrics records every event for inspection.
type fakeMetrics struct {
	mu       sync.Mutex
	counters map[string]float64
	hist     map[string][]float64
	gauges   map[string]float64
}

func newFakeMetrics() *fakeMetrics {
	return &fakeMetrics{
		counters: map[string]float64{},
		hist:     map[string][]float64{},
		gauges:   map[string]float64{},
	}
}

func metricKey(name string, labels map[string]string) string {
	if len(labels) == 0 {
		return name
	}
	parts := []string{name}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	// stable order
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, ",")
}

func (m *fakeMetrics) Inc(name string, labels map[string]string) {
	m.Add(name, 1, labels)
}
func (m *fakeMetrics) Add(name string, d float64, labels map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counters[metricKey(name, labels)] += d
}
func (m *fakeMetrics) Observe(name string, v float64, labels map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hist[metricKey(name, labels)] = append(m.hist[metricKey(name, labels)], v)
}
func (m *fakeMetrics) Set(name string, v float64, labels map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gauges[metricKey(name, labels)] = v
}

func (m *fakeMetrics) counter(name string, labels map[string]string) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counters[metricKey(name, labels)]
}

// frameBytes wraps body in MLLP markers.
func frameBytes(body string) []byte {
	out := make([]byte, 0, len(body)+3)
	out = append(out, frameStart)
	out = append(out, body...)
	out = append(out, frameEnd1, frameEnd2)
	return out
}

// readFrame reads up to 0x1C 0x0D from conn and returns the inter-marker bytes.
func readFrame(t *testing.T, conn net.Conn) []byte {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	buf := make([]byte, 0, 256)
	one := make([]byte, 1)
	for {
		n, err := conn.Read(one)
		if err != nil {
			t.Fatalf("read frame: %v (so far: %q)", err, buf)
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

// Standard fixtures.
const (
	sampleORU = "MSH|^~\\&|SNDR|FAC|RCVR|RFAC|20240101010101||ORU^R01|MSG-12345|P|2.5\rPID|||x\r"
	sampleADT = "MSH|^~\\&|SNDR|FAC|RCVR|RFAC|20240101010101||ADT^A01|MSG-A001|P|2.5\rPID|||x\r"
	sampleORM = "MSH|^~\\&|SNDR|FAC|RCVR|RFAC|20240101010101||ORM^O01|MSG-O001|P|2.5\rPID|||x\r"
)

func defaultConfig(eps ...EndpointConfig) ListenerConfig {
	return ListenerConfig{
		Endpoints:           eps,
		MaxMessageBytes:     1 << 20,
		ReadIdleTimeout:     5 * time.Second,
		PersistTimeout:      2 * time.Second,
		NackThenDropAfter:   5,
		ShutdownDrainGrace:  2 * time.Second,
		InflightCapPerConn:  64,
	}
}

// ----- Tests -----

// Single round-trip via in-memory pipe: send framed message, receive ACK,
// assert persister was called and the row carries the right metadata.
func TestListener_HandleConn_RoundTrip(t *testing.T) {
	p := &fakePersister{}
	m := newFakeMetrics()
	ep := EndpointConfig{Name: "adt-feed"}
	cfg := defaultConfig(ep)

	server, client := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		HandleConnection(context.Background(), server, ep, cfg, p, m, "127.0.0.1:1234")
	}()

	if _, err := client.Write(frameBytes(sampleORU)); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	ack := readFrame(t, client)
	if !strings.HasPrefix(string(ack), "MSH|") {
		t.Fatalf("ACK must start with MSH|; got %q", ack)
	}
	if !strings.Contains(string(ack), "MSA|AA|MSG-12345") {
		t.Fatalf("ACK must contain MSA|AA|MSG-12345; got %q", ack)
	}

	_ = client.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("connection handler did not exit after client close")
	}

	rows := p.Rows()
	if len(rows) != 1 {
		t.Fatalf("want 1 persisted row, got %d", len(rows))
	}
	row := rows[0]
	if row.ListenerEndpoint != "adt-feed" {
		t.Fatalf("row.ListenerEndpoint = %q, want adt-feed", row.ListenerEndpoint)
	}
	if row.MLLPMessageID != "MSG-12345" {
		t.Fatalf("row.MLLPMessageID = %q, want MSG-12345", row.MLLPMessageID)
	}
	if row.PeerAddr != "127.0.0.1:1234" {
		t.Fatalf("row.PeerAddr = %q, want 127.0.0.1:1234", row.PeerAddr)
	}
	if row.CorrelationID == uuid.Nil {
		t.Fatalf("row.CorrelationID must be assigned (non-nil UUID)")
	}
	if row.ID == uuid.Nil {
		t.Fatalf("row.ID must be assigned (non-nil UUID)")
	}
	if string(row.Body) != sampleORU {
		t.Fatalf("row.Body mismatch:\n got: %q\nwant: %q", row.Body, sampleORU)
	}

	// Counters
	if got := m.counter(MetricMessagesReceivedTotal, map[string]string{"listener_endpoint": "adt-feed", "peer_addr": "127.0.0.1:1234"}); got != 1 {
		t.Fatalf("received_total = %v, want 1", got)
	}
	if got := m.counter(MetricMessagesAckedTotal, map[string]string{"listener_endpoint": "adt-feed", "outcome": OutcomeAA}); got != 1 {
		t.Fatalf("acked_total{outcome=AA} = %v, want 1", got)
	}
}

// Persister must be called BEFORE ACK is written. We enforce ordering using
// a hook that signals when persist returns and a separate goroutine that
// reads the ACK; the read must not complete before the hook fires.
func TestListener_PersistBeforeAck(t *testing.T) {
	persistDone := make(chan struct{})
	p := &fakePersister{
		afterHook: func(ctx context.Context, _ QueueRow) {
			close(persistDone)
		},
		delay: 100 * time.Millisecond,
	}
	m := newFakeMetrics()
	ep := EndpointConfig{Name: "adt-feed"}
	cfg := defaultConfig(ep)

	server, client := net.Pipe()
	defer client.Close()

	go HandleConnection(context.Background(), server, ep, cfg, p, m, "127.0.0.1:9000")

	if _, err := client.Write(frameBytes(sampleORU)); err != nil {
		t.Fatalf("write: %v", err)
	}

	ackRead := make(chan []byte, 1)
	go func() {
		ackRead <- readFrame(t, client)
	}()

	// Persist must complete first.
	select {
	case <-persistDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("persist hook never fired")
	}

	// ACK comes after.
	select {
	case ack := <-ackRead:
		if !strings.Contains(string(ack), "MSA|AA|") {
			t.Fatalf("expected AA ACK after persist; got %q", ack)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("ACK not received after persist")
	}
}

// Persister failure -> NACK (AE) when on_persist_fail is "nack" (default).
func TestListener_PersistFailure_NACKsAE(t *testing.T) {
	p := &fakePersister{err: errors.New("simulated transient failure")}
	m := newFakeMetrics()
	ep := EndpointConfig{Name: "lab-results"}
	cfg := defaultConfig(ep)
	cfg.OnPersistFail = OnPersistFailNack

	server, client := net.Pipe()
	defer client.Close()

	go HandleConnection(context.Background(), server, ep, cfg, p, m, "10.0.0.5:50001")
	if _, err := client.Write(frameBytes(sampleORU)); err != nil {
		t.Fatalf("write: %v", err)
	}
	ack := readFrame(t, client)
	if !strings.Contains(string(ack), "MSA|AE|MSG-12345") {
		t.Fatalf("expected MSA|AE|MSG-12345 NACK; got %q", ack)
	}
	if got := m.counter(MetricMessagesAckedTotal, map[string]string{"listener_endpoint": "lab-results", "outcome": OutcomeAE}); got != 1 {
		t.Fatalf("acked_total{outcome=AE} = %v, want 1", got)
	}
	if len(p.Rows()) != 0 {
		t.Fatalf("no rows should have persisted; got %d", len(p.Rows()))
	}
}

// on_persist_fail: "drop" -> connection is closed, no NACK is written.
func TestListener_PersistFailure_DropsConnection(t *testing.T) {
	p := &fakePersister{err: errors.New("simulated transient failure")}
	m := newFakeMetrics()
	ep := EndpointConfig{Name: "adt-feed"}
	cfg := defaultConfig(ep)
	cfg.OnPersistFail = OnPersistFailDrop

	server, client := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		HandleConnection(context.Background(), server, ep, cfg, p, m, "10.0.0.7:1")
	}()

	if _, err := client.Write(frameBytes(sampleORU)); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Read should hit EOF / closed-pipe quickly without an ACK frame coming back.
	if err := client.SetReadDeadline(time.Now().Add(1500 * time.Millisecond)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	buf := make([]byte, 1)
	n, err := client.Read(buf)
	if err == nil && n > 0 {
		t.Fatalf("expected connection drop on persist failure; got byte %x", buf[0])
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("handler did not exit after drop")
	}

	if got := m.counter(MetricDropForPersistFails, map[string]string{"listener_endpoint": "adt-feed"}); got < 1 {
		t.Fatalf("expected drop_for_persist_failures >= 1, got %v", got)
	}
}

// Multiple concurrent endpoints, each receiving its own message stream.
func TestListener_MultipleEndpointsConcurrent(t *testing.T) {
	p := &fakePersister{}
	m := newFakeMetrics()

	endpoints := []EndpointConfig{
		{Name: "ep1"},
		{Name: "ep2"},
		{Name: "ep3"},
	}
	cfg := defaultConfig(endpoints...)

	pairs := make([]struct {
		serverConn, clientConn net.Conn
		endpoint               EndpointConfig
	}, len(endpoints))
	for i, ep := range endpoints {
		s, c := net.Pipe()
		pairs[i] = struct {
			serverConn, clientConn net.Conn
			endpoint               EndpointConfig
		}{s, c, ep}
	}

	var wg sync.WaitGroup
	for i := range pairs {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			HandleConnection(context.Background(), pairs[idx].serverConn, pairs[idx].endpoint, cfg, p, m, fmt.Sprintf("10.0.%d.1:1234", idx))
		}(i)
	}

	const perConn = 5
	var sentTotal int64
	for i := range pairs {
		go func(idx int) {
			defer pairs[idx].clientConn.Close()
			for j := 0; j < perConn; j++ {
				body := fmt.Sprintf("MSH|^~\\&|SNDR|FAC|||20240101||ADT^A01|EP%d-MSG-%d|P|2.5\r", idx, j)
				if _, err := pairs[idx].clientConn.Write(frameBytes(body)); err != nil {
					t.Errorf("ep%d write %d: %v", idx, j, err)
					return
				}
				_ = readFrame(t, pairs[idx].clientConn)
				atomic.AddInt64(&sentTotal, 1)
			}
		}(i)
	}

	// Wait for all clients to finish then close their server side.
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt64(&sentTotal) < int64(len(pairs)*perConn) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt64(&sentTotal) != int64(len(pairs)*perConn) {
		t.Fatalf("sentTotal = %d, want %d", sentTotal, len(pairs)*perConn)
	}

	wg.Wait()

	// Every row should be present, partitioned by endpoint.
	rows := p.Rows()
	if len(rows) != len(pairs)*perConn {
		t.Fatalf("rows = %d, want %d", len(rows), len(pairs)*perConn)
	}
	byEP := map[string]int{}
	for _, r := range rows {
		byEP[r.ListenerEndpoint]++
	}
	for _, ep := range endpoints {
		if byEP[ep.Name] != perConn {
			t.Fatalf("endpoint %s rows = %d, want %d", ep.Name, byEP[ep.Name], perConn)
		}
	}
}

// allowed_message_types filter rejects unknown types with NACK.
func TestListener_AllowedMessageTypes_Filter(t *testing.T) {
	p := &fakePersister{}
	m := newFakeMetrics()
	ep := EndpointConfig{
		Name:                "adt-feed",
		AllowedMessageTypes: []string{"ADT"},
	}
	cfg := defaultConfig(ep)

	server, client := net.Pipe()
	defer client.Close()

	go HandleConnection(context.Background(), server, ep, cfg, p, m, "127.0.0.1:5050")

	// Send ORM (disallowed).
	if _, err := client.Write(frameBytes(sampleORM)); err != nil {
		t.Fatalf("write: %v", err)
	}
	ack := readFrame(t, client)
	if !strings.Contains(string(ack), "MSA|AE|MSG-O001") {
		t.Fatalf("expected MSA|AE|MSG-O001 NACK; got %q", ack)
	}
	if len(p.Rows()) != 0 {
		t.Fatalf("disallowed type must not be persisted; got %d rows", len(p.Rows()))
	}

	// Send ADT (allowed).
	if _, err := client.Write(frameBytes(sampleADT)); err != nil {
		t.Fatalf("write: %v", err)
	}
	ack = readFrame(t, client)
	if !strings.Contains(string(ack), "MSA|AA|MSG-A001") {
		t.Fatalf("allowed type must be ACKed; got %q", ack)
	}
	if got := len(p.Rows()); got != 1 {
		t.Fatalf("allowed type must be persisted exactly once; got %d", got)
	}
}

// Graceful shutdown: in-flight persist completes, then handler exits without
// accepting new frames.
func TestListener_ShutdownDrainsInFlight(t *testing.T) {
	releasePersist := make(chan struct{})
	persistEntered := make(chan struct{}, 1)
	persistedRows := int64(0)

	p := &fakePersister{
		persistFn: func(ctx context.Context, row QueueRow) error {
			select {
			case persistEntered <- struct{}{}:
			default:
			}
			select {
			case <-releasePersist:
			case <-ctx.Done():
				return ctx.Err()
			}
			atomic.AddInt64(&persistedRows, 1)
			return nil
		},
	}
	m := newFakeMetrics()
	ep := EndpointConfig{Name: "adt-feed"}
	cfg := defaultConfig(ep)
	cfg.PersistTimeout = 5 * time.Second // allow our manual release.

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, client := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		HandleConnection(ctx, server, ep, cfg, p, m, "127.0.0.1:6000")
	}()

	if _, err := client.Write(frameBytes(sampleORU)); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Wait for the handler to enter persist.
	select {
	case <-persistEntered:
	case <-time.After(2 * time.Second):
		t.Fatalf("persist never entered")
	}

	// Initiate graceful shutdown by canceling the context.
	cancel()

	// Release the in-flight persist; the handler must finish persisting
	// then exit.
	close(releasePersist)

	// Read the ACK that completes the in-flight persist's contract.
	ack := readFrame(t, client)
	if !strings.Contains(string(ack), "MSA|AA|MSG-12345") {
		t.Fatalf("in-flight frame must be ACKed before shutdown exits; got %q", ack)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("handler did not exit after shutdown drain")
	}

	if atomic.LoadInt64(&persistedRows) != 1 {
		t.Fatalf("in-flight row not persisted; got %d", persistedRows)
	}
}
