// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/mllp"
)

// MLLP control bytes per LLD §4 (also defined in internal/mllp/framer.go).
const (
	mllpVT = 0x0B
	mllpFS = 0x1C
	mllpCR = 0x0D
)

// goodMSH builds a minimally well-formed HL7 v2 MSH segment whose
// MSH-10 (control id) is the supplied id. The body is wrapped by the
// caller in MLLP framing.
func goodMSH(id string) []byte {
	// Full MSH header with MSH-1 = "|" and MSH-2 = "^~\&". MSH-10 is
	// id; everything else is fixed. The receiver only requires MSH-1,
	// MSH-2, MSH-9 (message type), MSH-10 (control id) to be present.
	return []byte(fmt.Sprintf(
		"MSH|^~\\&|SENDER|FAC|RECV|RECF|20260101120000||ADT^A01|%s|P|2.5\r"+
			"PID|1||MRN-1||DOE^JOHN\r",
		id,
	))
}

// frameMLLP wraps body in 0x0B ... 0x1C 0x0D framing.
func frameMLLP(body []byte) []byte {
	out := make([]byte, 0, len(body)+3)
	out = append(out, mllpVT)
	out = append(out, body...)
	out = append(out, mllpFS, mllpCR)
	return out
}

// startMLLPListenerForTest brings up a real internal/mllp Listener with
// a generic e2eFakePersister. Returns the listener (caller calls
// Shutdown) and its bound address.
func startMLLPListenerForTest(t *testing.T, cfg mllp.ListenerConfig, p mllp.Persister, log mllp.Logger) (*mllp.Listener, net.Addr) {
	t.Helper()
	if p == nil {
		p = e2eFakePersister{}
	}
	if log == nil {
		log = newE2EMLLPLogger()
	}
	l := mllp.New(cfg, p, nil, log)
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("listener start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = l.Shutdown(ctx)
	})
	endpointName := cfg.Endpoints[0].Name
	addr := l.Addr(endpointName)
	if addr == nil {
		t.Fatalf("listener has no bound addr for %s", endpointName)
	}
	return l, addr
}

// dialAndWaitClose returns when the server closes the connection (read
// returns EOF / reset) or the deadline elapses. Returns (closed, error).
// closed=true means the server closed; err is non-nil only when the
// read returned an unexpected error.
func dialAndWaitClose(t *testing.T, addr string, write []byte, deadline time.Duration) (closed bool) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	if len(write) > 0 {
		if _, err := c.Write(write); err != nil {
			t.Logf("write returned %v (acceptable if server already dropped)", err)
		}
	}
	_ = c.SetReadDeadline(time.Now().Add(deadline))
	buf := make([]byte, 4096)
	n, rerr := c.Read(buf)
	if rerr != nil {
		if errors.Is(rerr, io.EOF) || isClosedConnTestErr(rerr) {
			return true
		}
		// Read returned with some other error (timeout etc.); treat it
		// as not-closed (test-side decides).
		t.Logf("read returned err=%v after n=%d", rerr, n)
		return false
	}
	// We got data. The data could be an ACK (test sent a valid frame).
	// Try a second read to see whether the server then closes.
	_ = c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, rerr2 := c.Read(buf[:64])
	if rerr2 != nil && (errors.Is(rerr2, io.EOF) || isClosedConnTestErr(rerr2)) {
		return true
	}
	return false
}

func isClosedConnTestErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "use of closed network connection") ||
		strings.Contains(s, "connection reset by peer") ||
		strings.Contains(s, "broken pipe")
}

// TestE2E_MLLP_TruncatedFrame_DropsConn verifies that an MLLP sender
// who writes only the start byte + a header but never closes the frame
// (no FS/CR pair) is eventually disconnected by the listener — no
// half-open connection persists. The relevant production safety is the
// per-message frame deadline (S-9.1) and ReadIdleTimeout, so the
// accepted conn cannot wedge.
func TestE2E_MLLP_TruncatedFrame_DropsConn(t *testing.T) {
	t.Parallel()
	logr := newE2EMLLPLogger()
	cfg := mllp.ListenerConfig{
		Endpoints:          []mllp.EndpointConfig{{Name: "adt-feed", Bind: "127.0.0.1:0"}},
		MaxMessageBytes:    4096,
		ReadIdleTimeout:    1 * time.Second, // short — we want the listener to bail
		PersistTimeout:     2 * time.Second,
		NackThenDropAfter:  5,
		ShutdownDrainGrace: 2 * time.Second,
	}
	_, addr := startMLLPListenerForTest(t, cfg, nil, logr)

	// Write the start byte + a partial body, then sit idle. No FS/CR.
	partial := append([]byte{mllpVT}, []byte("MSH|^~\\&|SENDER|FAC|RECV|")...)
	closed := dialAndWaitClose(t, addr.String(), partial, 5*time.Second)
	if !closed {
		t.Fatalf("server did not close the connection within deadline")
	}
}

// TestE2E_MLLP_OversizedFrame_DropsConn verifies that a sender who
// overshoots MaxMessageBytes is disconnected and the
// fhir_subs_mllp_malformed_total{reason=oversized_message} metric
// fires. Without this drop, a single misbehaving peer could exhaust
// listener memory by streaming a multi-GiB "frame".
func TestE2E_MLLP_OversizedFrame_DropsConn(t *testing.T) {
	t.Parallel()
	logr := newE2EMLLPLogger()
	const maxBody = 512
	cfg := mllp.ListenerConfig{
		Endpoints:          []mllp.EndpointConfig{{Name: "adt-feed", Bind: "127.0.0.1:0"}},
		MaxMessageBytes:    maxBody,
		ReadIdleTimeout:    5 * time.Second,
		PersistTimeout:     2 * time.Second,
		NackThenDropAfter:  5,
		ShutdownDrainGrace: 2 * time.Second,
	}
	_, addr := startMLLPListenerForTest(t, cfg, nil, logr)

	// Send the start byte + 4*maxBody of junk bytes. The framer's
	// pendingExceeded()/maxBody guard fires at 2*maxBody and emits
	// MalformedEvent{Reason: ReasonOversizedMessage}; the connection
	// drops.
	huge := append([]byte{mllpVT}, bytes.Repeat([]byte("X"), 4*maxBody)...)
	closed := dialAndWaitClose(t, addr.String(), huge, 5*time.Second)
	if !closed {
		t.Fatalf("server did not close after oversized frame")
	}
	// Allow a brief moment for the malformed metric to be recorded.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if logr.hasWarn("malformed") || logr.hasWarn("oversized") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("expected WARN log mentioning malformed/oversized; got %v", logr.snapshot())
}

// TestE2E_MLLP_Slowloris_AcceptsButDropsAfterIdle verifies that a
// connecting peer that writes nothing is dropped via ReadIdleTimeout.
// "Slowloris" is the family of attacks where many half-open connections
// pin server resources without ever sending data; the listener's
// timeout is what bounds it.
func TestE2E_MLLP_Slowloris_AcceptsButDropsAfterIdle(t *testing.T) {
	t.Parallel()
	cfg := mllp.ListenerConfig{
		Endpoints:          []mllp.EndpointConfig{{Name: "adt-feed", Bind: "127.0.0.1:0"}},
		MaxMessageBytes:    1 << 20,
		ReadIdleTimeout:    700 * time.Millisecond, // short
		PersistTimeout:     2 * time.Second,
		NackThenDropAfter:  5,
		ShutdownDrainGrace: 2 * time.Second,
	}
	_, addr := startMLLPListenerForTest(t, cfg, nil, nil)

	// Connect, send NOTHING.
	closed := dialAndWaitClose(t, addr.String(), nil, 4*time.Second)
	if !closed {
		t.Fatalf("server did not drop the silent client within idle timeout")
	}
}

// TestE2E_MLLP_RapidConnectDisconnect_NoConnLeak fires N
// connect/disconnect cycles without ever sending data. After all
// closures, the listener's Status().ActiveConnections must drop back to
// 0. A buggy accept loop that leaked goroutines on premature client
// close would manifest as ActiveConnections > 0 at the end.
//
// This is a much smaller version of the "1000 cycles fd leak" check —
// 100 cycles is enough to expose a per-conn leak in a CI-bounded test.
func TestE2E_MLLP_RapidConnectDisconnect_NoConnLeak(t *testing.T) {
	t.Parallel()
	cfg := mllp.ListenerConfig{
		Endpoints:          []mllp.EndpointConfig{{Name: "adt-feed", Bind: "127.0.0.1:0"}},
		MaxMessageBytes:    1 << 20,
		ReadIdleTimeout:    2 * time.Second,
		PersistTimeout:     1 * time.Second,
		NackThenDropAfter:  5,
		ShutdownDrainGrace: 2 * time.Second,
	}
	l, addr := startMLLPListenerForTest(t, cfg, nil, nil)

	const cycles = 100
	for i := 0; i < cycles; i++ {
		c, err := net.DialTimeout("tcp", addr.String(), 1*time.Second)
		if err != nil {
			t.Fatalf("dial cycle %d: %v", i, err)
		}
		_ = c.Close()
	}

	// Wait for the listener's per-conn goroutines to notice the EOFs and
	// exit. The accept loop reads at-most ReadIdleTimeout-ish before
	// noticing.
	deadline := time.Now().Add(5 * time.Second)
	var last int
	for time.Now().Before(deadline) {
		st := l.Status()
		if len(st.Endpoints) == 0 {
			break
		}
		last = st.Endpoints[0].ActiveConnections
		if last == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("ActiveConnections after %d connect/close cycles: %d (want 0)", cycles, last)
}

// TestE2E_MLLP_OverConnectionLimit_RefusesGracefully verifies that
// dialing 2x more connections than MaxConnections does NOT break the
// listener: the first MaxConnections succeed (held open), the rest are
// gracefully closed, and after all clients hang up the cap-tracker
// drains so a fresh client can connect again.
//
// This complements TestE2E_MLLP_MaxConnections_Refuses (which tests
// just one extra). The pressure test is what catches admission-control
// bugs that only manifest under sustained overload.
func TestE2E_MLLP_OverConnectionLimit_RefusesGracefully(t *testing.T) {
	t.Parallel()

	const maxConns = 4
	const overload = 2 * maxConns
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
	l, addr := startMLLPListenerForTest(t, cfg, nil, logr)

	// Hold MaxConnections open.
	held := make([]net.Conn, 0, maxConns)
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()
	for i := 0; i < maxConns; i++ {
		c, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
		if err != nil {
			t.Fatalf("dial primary %d: %v", i, err)
		}
		held = append(held, c)
	}

	// Wait until the listener registers the held connections.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if l.Status().Endpoints[0].ActiveConnections >= maxConns {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Fire overload-many extras concurrently. Each must terminate
	// quickly; the listener must NOT deadlock.
	var wg sync.WaitGroup
	wg.Add(overload)
	for i := 0; i < overload; i++ {
		go func() {
			defer wg.Done()
			c, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
			if err != nil {
				return // OS-level reject is acceptable
			}
			_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
			buf := make([]byte, 1)
			_, _ = c.Read(buf) // EOF expected
			_ = c.Close()
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatalf("over-limit dialers did not finish in 8s — listener may be deadlocked")
	}

	if !logr.hasWarn("max_connections") {
		t.Errorf("expected WARN log mentioning max_connections; got %v", logr.snapshot())
	}

	// Now release the held connections and verify a fresh client can
	// connect and exchange a frame again — i.e., the cap-tracker
	// recovered its slots.
	for _, c := range held {
		_ = c.Close()
	}
	held = held[:0]

	// Wait for active count to drop.
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if l.Status().Endpoints[0].ActiveConnections == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := l.Status().Endpoints[0].ActiveConnections; got != 0 {
		t.Errorf("after release, ActiveConnections=%d; want 0", got)
	}

	// Fresh client should be admitted.
	c, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
	if err != nil {
		t.Fatalf("post-release dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	// Send a valid MSH frame and expect an ACK back. This proves the
	// post-release accept path is fully functional.
	if _, err := c.Write(frameMLLP(goodMSH("recovery-1"))); err != nil {
		t.Fatalf("post-release write: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 4096)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("post-release read ACK: %v", err)
	}
	// The ACK begins with VT and ends with FS/CR. The body is an MSA.
	if n < 5 || buf[0] != mllpVT {
		t.Errorf("expected MLLP ACK frame, got %d bytes %q", n, buf[:n])
	}
	if !bytes.Contains(buf[:n], []byte("MSA")) {
		t.Errorf("expected MSA segment in ACK, got %q", buf[:n])
	}
}

// TestE2E_MLLP_MaxConnectionsPerIP_Refuses verifies the per-IP cap
// (B-19): a single peer cannot monopolize accept-loop capacity. Dial
// 1+MaxConnectionsPerIP from the same loopback peer; the last must be
// dropped with a per-ip log.
func TestE2E_MLLP_MaxConnectionsPerIP_Refuses(t *testing.T) {
	t.Parallel()
	const perIP = 2
	logr := newE2EMLLPLogger()
	cfg := mllp.ListenerConfig{
		Endpoints:           []mllp.EndpointConfig{{Name: "adt-feed", Bind: "127.0.0.1:0"}},
		MaxMessageBytes:     1 << 20,
		ReadIdleTimeout:     5 * time.Second,
		PersistTimeout:      2 * time.Second,
		NackThenDropAfter:   5,
		ShutdownDrainGrace:  2 * time.Second,
		MaxConnectionsPerIP: perIP,
	}
	l, addr := startMLLPListenerForTest(t, cfg, nil, logr)

	held := make([]net.Conn, 0, perIP)
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()
	for i := 0; i < perIP; i++ {
		c, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		held = append(held, c)
	}
	// Wait for active count to register.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if l.Status().Endpoints[0].ActiveConnections >= perIP {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Excess should be dropped.
	extra, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
	if err == nil {
		_ = extra.SetReadDeadline(time.Now().Add(1 * time.Second))
		buf := make([]byte, 1)
		_, _ = extra.Read(buf) // expect EOF
		_ = extra.Close()
	}
	// Allow the warn log to flush.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if logr.hasWarn("max_connections_per_ip") || logr.hasWarn("per_ip") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("expected WARN log mentioning per-ip cap; got %v", logr.snapshot())
}

// TestE2E_MLLP_PersistFailures_NackThenDrop verifies the production
// "nack then drop after N consecutive persist failures" ramp (LLD §5.6).
// We use a persister that always returns ErrPersistTransient. Senders
// see the listener NACK the first NackThenDropAfter messages and then
// disconnect. Without this ramp, a flaky persister would let a sender
// retry indefinitely — drowning operator logs.
func TestE2E_MLLP_PersistFailures_NackThenDrop(t *testing.T) {
	t.Parallel()
	const ramp = 3
	logr := newE2EMLLPLogger()
	cfg := mllp.ListenerConfig{
		Endpoints:          []mllp.EndpointConfig{{Name: "adt-feed", Bind: "127.0.0.1:0"}},
		MaxMessageBytes:    1 << 20,
		ReadIdleTimeout:    5 * time.Second,
		PersistTimeout:     2 * time.Second,
		NackThenDropAfter:  ramp,
		ShutdownDrainGrace: 2 * time.Second,
		OnPersistFail:      mllp.OnPersistFailNack,
	}

	failer := &alwaysFailingPersister{}
	_, addr := startMLLPListenerForTest(t, cfg, failer, logr)

	c, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Send ramp+2 messages. We expect ramp NACKs, then drop. After drop,
	// further writes return errors.
	nacks := 0
	dropped := false
	for i := 0; i < ramp+2; i++ {
		_, werr := c.Write(frameMLLP(goodMSH(fmt.Sprintf("p-%d", i))))
		if werr != nil {
			dropped = true
			break
		}
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, 4096)
		n, rerr := c.Read(buf)
		if rerr != nil {
			dropped = true
			break
		}
		// MSA|AE or MSA|AR is a NACK; MSA|AA is an ACK. We need any
		// MSA followed by A[ER].
		if bytes.Contains(buf[:n], []byte("MSA|AE")) ||
			bytes.Contains(buf[:n], []byte("MSA|AR")) {
			nacks++
		}
	}
	if nacks < ramp {
		t.Errorf("got %d NACKs; want at least %d", nacks, ramp)
	}
	if !dropped {
		// Try one more write — connection may have just closed
		// asynchronously.
		_, werr := c.Write(frameMLLP(goodMSH("post-drop")))
		_ = c.SetReadDeadline(time.Now().Add(1 * time.Second))
		buf := make([]byte, 64)
		_, rerr := c.Read(buf)
		if werr == nil && rerr == nil {
			t.Errorf("expected connection drop after %d consecutive persist failures", ramp)
		}
	}
	if persistCalls := failer.calls.Load(); persistCalls < int64(ramp) {
		t.Errorf("persister called only %d times; want >= %d", persistCalls, ramp)
	}
}

// alwaysFailingPersister returns mllp.ErrPersistTransient for every
// call, used to drive the NACK-then-drop ramp.
type alwaysFailingPersister struct {
	calls atomic.Int64
}

func (p *alwaysFailingPersister) Persist(ctx context.Context, _ mllp.QueueRow) error {
	p.calls.Add(1)
	return mllp.ErrPersistTransient
}
