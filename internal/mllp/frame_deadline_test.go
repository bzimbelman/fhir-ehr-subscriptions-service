// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// S-9.1: A peer that streams the start byte (and optionally a few body
// bytes) but never finishes the frame must be dropped at
// FrameAssemblyTimeout. Before this fix the slowloris-style peer kept the
// connection slot tied up until the framer's pending-byte cap (S-9.4)
// tripped — i.e. the peer could send an unbounded stream of body bytes at
// up to 2× MaxMessageBytes before the framer killed it. With the
// FrameAssemblyTimeout in place the connection drops after the configured
// per-message budget regardless of whether bytes are still arriving.
func TestFrameAssemblyTimeout_StalledPeer_Dropped(t *testing.T) {
	p := &fakePersister{}
	m := newFakeMetrics()
	ep := EndpointConfig{Name: "adt-feed"}
	cfg := defaultConfig(ep)
	// Disable idle timeout so the test isolates the frame-assembly path.
	cfg.ReadIdleTimeout = 0
	cfg.FrameAssemblyTimeout = 200 * time.Millisecond

	server, client := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		HandleConnection(context.Background(), server, ep, cfg, p, m, "127.0.0.1:5050")
		close(done)
	}()

	// Send a frame start byte plus a partial MSH that never ends. The
	// framer transitions to stateOpen and sits there.
	partial := []byte{frameStart}
	partial = append(partial, []byte("MSH|^~\\&|SNDR|FAC|||20240101||ADT^A01|STALL|P|2.5\r")...)
	if _, err := client.Write(partial); err != nil {
		t.Fatalf("write partial: %v", err)
	}

	select {
	case <-done:
		// good — connection dropped
	case <-time.After(2 * time.Second):
		t.Fatalf("connection still open well after FrameAssemblyTimeout (%s)", cfg.FrameAssemblyTimeout)
	}

	got := m.counter(MetricFrameDeadlineExceeded, map[string]string{
		"listener_endpoint": ep.Name,
	})
	if got != 1 {
		t.Fatalf("expected fhir_subs_mllp_frame_deadline_exceeded=1; got %v (keys=%v)", got, m.allCounterKeys())
	}
}

// S-9.1: An idle peer that has NOT started a frame must NOT trigger the
// frame-assembly deadline (only the read-idle timeout governs that case).
// Otherwise an EHR connection sitting between bursts would close on the
// first 30-second lull.
func TestFrameAssemblyTimeout_IdleNoFrame_NotDropped(t *testing.T) {
	p := &fakePersister{}
	m := newFakeMetrics()
	ep := EndpointConfig{Name: "adt-feed"}
	cfg := defaultConfig(ep)
	cfg.ReadIdleTimeout = 0
	cfg.FrameAssemblyTimeout = 100 * time.Millisecond

	server, client := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		HandleConnection(context.Background(), server, ep, cfg, p, m, "127.0.0.1:5050")
		close(done)
	}()

	// Wait past the assembly timeout without sending anything.
	select {
	case <-done:
		t.Fatalf("idle connection (no frame in flight) was dropped under FrameAssemblyTimeout")
	case <-time.After(300 * time.Millisecond):
		// good
	}

	// Cleanly close the client so the handler exits.
	_ = client.Close()
	<-done

	got := m.counter(MetricFrameDeadlineExceeded, map[string]string{
		"listener_endpoint": ep.Name,
	})
	if got != 0 {
		t.Fatalf("expected fhir_subs_mllp_frame_deadline_exceeded=0; got %v", got)
	}
}

// S-9.1: A peer that completes a frame, then stalls mid-way through a
// SECOND frame must reset the assembly deadline at the boundary. The
// second frame's assembly timer starts when the second 0x0B arrives, not
// from when the first frame began.
func TestFrameAssemblyTimeout_DeadlineResetsAfterCompleteFrame(t *testing.T) {
	p := &fakePersister{}
	m := newFakeMetrics()
	ep := EndpointConfig{Name: "adt-feed"}
	cfg := defaultConfig(ep)
	cfg.ReadIdleTimeout = 0
	cfg.FrameAssemblyTimeout = 300 * time.Millisecond

	server, client := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		HandleConnection(context.Background(), server, ep, cfg, p, m, "127.0.0.1:5050")
		close(done)
	}()

	// First: send a full valid frame, read the AA ACK.
	body := "MSH|^~\\&|SNDR|FAC|||20240101||ADT^A01|OK1|P|2.5\rPID|||x\r"
	if _, err := client.Write(frameBytes(body)); err != nil {
		t.Fatalf("write frame 1: %v", err)
	}
	ack := readFrame(t, client)
	if !strings.Contains(string(ack), "MSA|AA|OK1") {
		t.Fatalf("frame 1: want AA ACK; got %q", ack)
	}

	// Sleep most of the FIRST frame's hypothetical budget, but less than
	// FrameAssemblyTimeout. If the deadline did NOT reset on the first
	// frame's completion, the partial second frame will fire too early.
	time.Sleep(200 * time.Millisecond)

	// Now begin a partial second frame and stall.
	partial := []byte{frameStart}
	partial = append(partial, []byte("MSH|^~\\&|SNDR|FAC|||20240101||ADT^A01|STALL|P|2.5\r")...)
	if _, err := client.Write(partial); err != nil {
		t.Fatalf("write partial frame 2: %v", err)
	}
	frame2Start := time.Now()

	select {
	case <-done:
		elapsed := time.Since(frame2Start)
		if elapsed < cfg.FrameAssemblyTimeout/2 {
			t.Fatalf("connection dropped %s after frame 2 began — deadline did not reset on frame 1 completion (timeout=%s)",
				elapsed, cfg.FrameAssemblyTimeout)
		}
	case <-time.After(cfg.FrameAssemblyTimeout * 5):
		t.Fatalf("connection still open well after frame 2 deadline")
	}

	got := m.counter(MetricFrameDeadlineExceeded, map[string]string{
		"listener_endpoint": ep.Name,
	})
	if got != 1 {
		t.Fatalf("expected fhir_subs_mllp_frame_deadline_exceeded=1; got %v", got)
	}
}

// S-9.1 / config: FrameAssemblyTimeout defaults to 30s when the operator
// leaves it unset.
func TestListenerConfig_FrameAssemblyTimeout_DefaultsTo30s(t *testing.T) {
	cfg := ListenerConfig{}.withDefaults()
	if cfg.FrameAssemblyTimeout != 30*time.Second {
		t.Fatalf("default FrameAssemblyTimeout = %s; want 30s", cfg.FrameAssemblyTimeout)
	}
}

// S-9.1: ErrFrameDeadline is exported so callers can identify the cause
// in logs / tests (see the typed-error contract enumerated alongside
// ErrPersistTransient / ErrPersistPermanent).
func TestErrFrameDeadline_IsExported(t *testing.T) {
	if ErrFrameDeadline == nil {
		t.Fatalf("ErrFrameDeadline is nil; expected exported sentinel")
	}
	if !strings.Contains(ErrFrameDeadline.Error(), "frame") {
		t.Fatalf("ErrFrameDeadline message %q should mention 'frame'", ErrFrameDeadline.Error())
	}
}
