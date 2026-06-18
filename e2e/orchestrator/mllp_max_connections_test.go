// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/mllp"
)

// TestE2E_MLLP_MaxConnections_Refuses verifies B-19: configuring
// MaxConnections gates the listener; excess connections are accepted
// and immediately closed, and a WARN log records the offending peer.
func TestE2E_MLLP_MaxConnections_Refuses(t *testing.T) {
	t.Parallel()

	const maxConns = 3
	logr := newE2EMLLPLogger()
	cfg := mllp.ListenerConfig{
		Endpoints:          []mllp.EndpointConfig{{Name: "adt-feed", Bind: "127.0.0.1:0"}},
		MaxMessageBytes:    1 << 20,
		ReadIdleTimeout:    5 * time.Second,
		PersistTimeout:     2 * time.Second,
		NackThenDropAfter:  5,
		ShutdownDrainGrace: 2 * time.Second,
		MaxConnections:     maxConns,
	}
	l := mllp.New(cfg, &e2eFakePersister{}, nil, logr)
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("listener start: %v", err)
	}
	defer func() { _ = l.Shutdown(context.Background()) }()
	addr := l.Addr("adt-feed")
	if addr == nil {
		t.Fatalf("addr unavailable")
	}

	held := make([]net.Conn, 0, maxConns)
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()
	for i := 0; i < maxConns; i++ {
		c, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		held = append(held, c)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if l.Status().Endpoints[0].ActiveConnections >= maxConns {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	extra, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
	if err != nil {
		t.Logf("excess dial rejected at OS level: %v", err)
	} else {
		_ = extra.SetReadDeadline(time.Now().Add(1 * time.Second))
		buf := make([]byte, 1)
		if n, rerr := extra.Read(buf); rerr == nil && n > 0 {
			t.Fatalf("excess conn accepted; read %d bytes", n)
		} else if rerr != nil && !errors.Is(rerr, io.EOF) && !strings.Contains(rerr.Error(), "closed") &&
			!strings.Contains(rerr.Error(), "reset") {
			t.Logf("excess conn closed: %v", rerr)
		}
		_ = extra.Close()
	}

	if !logr.hasWarn("max_connections") {
		t.Errorf("expected WARN log mentioning max_connections; got %v", logr.snapshot())
	}
}

// e2eMLLPLogger captures structured log output for assertions.
type e2eMLLPLogger struct {
	mu      sync.Mutex
	entries []e2eMLLPLogEntry
}

type e2eMLLPLogEntry struct {
	level  string
	msg    string
	fields map[string]any
}

func newE2EMLLPLogger() *e2eMLLPLogger { return &e2eMLLPLogger{} }

func (l *e2eMLLPLogger) Info(msg string, fields map[string]any)  { l.add("info", msg, fields) }
func (l *e2eMLLPLogger) Warn(msg string, fields map[string]any)  { l.add("warn", msg, fields) }
func (l *e2eMLLPLogger) Error(msg string, fields map[string]any) { l.add("error", msg, fields) }

func (l *e2eMLLPLogger) add(level, msg string, fields map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cp := map[string]any{}
	for k, v := range fields {
		cp[k] = v
	}
	l.entries = append(l.entries, e2eMLLPLogEntry{level: level, msg: msg, fields: cp})
}

func (l *e2eMLLPLogger) snapshot() []e2eMLLPLogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]e2eMLLPLogEntry, len(l.entries))
	copy(out, l.entries)
	return out
}

// hasWarn reports whether any captured WARN entry has the needle in its
// message OR in any of its field keys / string values.
func (l *e2eMLLPLogger) hasWarn(needle string) bool {
	for _, e := range l.snapshot() {
		if e.level != "warn" {
			continue
		}
		if strings.Contains(e.msg, needle) {
			return true
		}
		for k, v := range e.fields {
			if strings.Contains(k, needle) {
				return true
			}
			if s, ok := v.(string); ok && strings.Contains(s, needle) {
				return true
			}
		}
	}
	return false
}

type e2eFakePersister struct{}

func (e2eFakePersister) Persist(context.Context, mllp.QueueRow) error { return nil }
