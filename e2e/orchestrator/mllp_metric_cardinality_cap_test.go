// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/mllp"
)

// TestB30_MLLPMetricCardinalityCap (B-30) drives 100 distinct hostile
// MSH-9 values through the production MLLP listener over a real
// TCP connection. Pre-fix: each rejected MessageType becomes its own
// label value on nack_total — Prometheus cardinality bomb.
//
// Post-fix: the listener whitelists "type" to the configured
// AllowedMessageTypes set and buckets everything else as "other". We
// assert the in-memory metrics emitter sees exactly one nack_total
// series with type=other carrying all 100 increments.
func TestB30_MLLPMetricCardinalityCap(t *testing.T) {
	if !shortTestcontainersOK() {
		// MLLP cardinality test does not need docker; runs purely in-process.
		// Kept under e2e build tag for grouping with the other B-30/34/35 tests.
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := newCountingMetrics()
	cfg := mllp.ListenerConfig{
		Endpoints: []mllp.EndpointConfig{{
			Name:                "b30-feed",
			Bind:                "127.0.0.1:0",
			AllowedMessageTypes: []string{"ADT", "ORU"},
		}},
		MaxMessageBytes:    1 << 20,
		ReadIdleTimeout:    5 * time.Second,
		PersistTimeout:     2 * time.Second,
		NackThenDropAfter:  10000,
		ShutdownDrainGrace: 2 * time.Second,
		InflightCapPerConn: 0,
	}
	persister := &nopPersister{}
	l := mllp.New(cfg, persister, m, nil)
	if err := l.Start(ctx); err != nil {
		t.Fatalf("listener start: %v", err)
	}
	defer func() { _ = l.Shutdown(context.Background()) }()

	addr := l.Addr("b30-feed")
	if addr == nil {
		t.Fatalf("listener has no bound address")
	}

	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	const N = 100
	for i := 0; i < N; i++ {
		body := fmt.Sprintf(
			"MSH|^~\\&|S|F|||20240101||EVIL%04d^X%04d|HOSTILE-%d|P|2.5\rPID|||x\r",
			i, i, i,
		)
		if _, err := conn.Write(framePayload(body)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		if _, err := readMLLPFrame(conn, 3*time.Second); err != nil {
			t.Fatalf("read ack %d: %v", i, err)
		}
	}

	// The frame counts past Read() must include a NACK per attempt.
	// Inspect counters directly: we expect exactly one nack_total
	// series with reason=message_type, and the type label must be
	// "other".
	keys := m.counterKeys("fhir_subs_mllp_nack_total")
	leaked := []string{}
	otherSeries := 0
	for _, k := range keys {
		if !strings.Contains(k, "reason=message_type") {
			continue
		}
		switch {
		case strings.Contains(k, "type=other"):
			otherSeries++
		case strings.Contains(k, "type=ADT"), strings.Contains(k, "type=ORU"):
			// allowed-set passthrough is fine
		default:
			leaked = append(leaked, k)
		}
	}
	if otherSeries != 1 {
		t.Fatalf("expected exactly 1 nack_total series with type=other; got %d (keys=%v)", otherSeries, keys)
	}
	if len(leaked) > 0 {
		t.Fatalf("hostile MSH-9 values leaked into label values; cardinality bomb still present: %v", leaked)
	}
	bucket := m.counter("fhir_subs_mllp_nack_total", map[string]string{
		"listener_endpoint": "b30-feed",
		"reason":            "message_type",
		"type":              "other",
	})
	if bucket != float64(N) {
		t.Fatalf("type=other bucket = %v; want %d", bucket, N)
	}
}

// nopPersister is a no-op Persister; the test rejects every frame at
// the allowed_message_types gate so persist is never called.
type nopPersister struct{}

func (nopPersister) Persist(_ context.Context, _ mllp.QueueRow) error { return nil }

// countingMetrics is an in-process MetricsEmitter with map-by-key
// counters. The test asserts on counter labels.
type countingMetrics struct {
	mu       sync.Mutex
	counters map[string]float64
}

func newCountingMetrics() *countingMetrics {
	return &countingMetrics{counters: map[string]float64{}}
}

func (m *countingMetrics) Inc(name string, labels map[string]string) {
	m.Add(name, 1, labels)
}
func (m *countingMetrics) Add(name string, d float64, labels map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counters[metricsKey(name, labels)] += d
}
func (m *countingMetrics) Observe(string, float64, map[string]string) {}
func (m *countingMetrics) Set(string, float64, map[string]string)     {}

func (m *countingMetrics) counter(name string, labels map[string]string) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counters[metricsKey(name, labels)]
}

func (m *countingMetrics) counterKeys(prefix string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0)
	for k := range m.counters {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out
}

func metricsKey(name string, labels map[string]string) string {
	if len(labels) == 0 {
		return name
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	// Stable order.
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	parts := []string{name}
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, ",")
}

// framePayload wraps body with MLLP markers (0x0B ... 0x1C 0x0D).
func framePayload(body string) []byte {
	out := make([]byte, 0, len(body)+3)
	out = append(out, 0x0B)
	out = append(out, body...)
	out = append(out, 0x1C, 0x0D)
	return out
}

// readMLLPFrame reads bytes off conn until it sees the MLLP terminator
// 0x1C 0x0D and returns the inter-marker payload.
func readMLLPFrame(conn net.Conn, timeout time.Duration) ([]byte, error) {
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	buf := make([]byte, 0, 256)
	one := make([]byte, 1)
	for {
		n, err := conn.Read(one)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			continue
		}
		buf = append(buf, one[0])
		if len(buf) >= 3 && buf[0] == 0x0B && buf[len(buf)-2] == 0x1C && buf[len(buf)-1] == 0x0D {
			return buf[1 : len(buf)-2], nil
		}
	}
}

// shortTestcontainersOK keeps the test runnable without docker for the
// purely in-process B-30 case. The harness gate would otherwise fail.
func shortTestcontainersOK() bool { return true }
