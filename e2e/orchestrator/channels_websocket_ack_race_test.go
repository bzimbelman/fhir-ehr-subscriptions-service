// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	codingws "github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/websocket"
)

// TestE2E_WebSocket_AckRace_NoPanic_NoLeak verifies B-18: under
// concurrent ack arrival + delivery deadline expiration the websocket
// channel must not panic on close-of-closed and must not leak
// goroutines per iteration. We compare the goroutine count before and
// after a tight 200-iteration deliver/ack loop and bound any leak to a
// small number of long-lived helpers (the read loop, ping loop).
func TestE2E_WebSocket_AckRace_NoPanic_NoLeak(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	tokens := newE2EFakeTokens(now)
	subID := uuid.New()
	tokens.mint("ack-race", subID, "client-a", now().Add(60*time.Second))

	ch, err := websocket.New(websocket.Options{
		Tokens:     tokens,
		Replayer:   e2eNoopReplayer{},
		Now:        now,
		AckTimeout: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("websocket.New: %v", err)
	}
	defer ch.Close()

	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()

	url := strings.Replace(srv.URL, "http://", "ws://", 1) + "/ws/subscriptions"
	conn, _, err := codingws.Dial(context.Background(), url, &codingws.DialOptions{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(codingws.StatusNormalClosure, "")

	bctx, bcancel := context.WithTimeout(context.Background(), 1*time.Second)
	if err := conn.Write(bctx, codingws.MessageText,
		[]byte(`{"type":"bind","subscriptionId":"`+subID.String()+`","token":"ack-race"}`)); err != nil {
		t.Fatalf("write bind: %v", err)
	}
	if _, _, err := conn.Read(bctx); err != nil {
		t.Fatalf("read bind: %v", err)
	}
	bcancel()

	startGor := runtime.NumGoroutine()

	const N = 200
	for i := 1; i <= N; i++ {
		seq := uint64(i)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			env := channel.NotificationEnvelope{
				SubscriptionID: subID,
				Sequence:       seq,
				BundleBytes:    []byte(`{"r":"x"}`),
				BundleKind:     channel.BundleEventNotification,
				ContentType:    channel.ContentTypeFHIRJSON,
				Deadline:       time.Now().Add(2 * time.Millisecond),
			}
			_, _ = ch.Deliver(context.Background(), env)
		}()
		readCtx, readCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		_, _, _ = conn.Read(readCtx)
		readCancel()

		ackCtx, ackCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		// Send three duplicate acks for the same sequence.
		for k := 0; k < 3; k++ {
			_ = conn.Write(ackCtx, codingws.MessageText,
				[]byte(`{"type":"ack","eventNumber":`+strconv.FormatUint(seq, 10)+`}`))
		}
		ackCancel()
		wg.Wait()
	}

	// Allow the server's pong / ping loop to settle and any cleanup
	// goroutines to exit. Close the channel so the per-session goroutines
	// (read + ping) join via WaitGroup before we sample (S-7 #9 changed
	// the runConnection contract to spawn readLoop in a dedicated
	// goroutine; the previous implementation reused the http handler
	// goroutine, so the absolute "<= 8" delta is no longer reliable
	// under parallel test scheduling — a Close-and-Wait gives the
	// stable post-shutdown count).
	_ = ch.Close()

	// runtime.NumGoroutine counts the entire process, not just this
	// channel. Under -race with parallel e2e tests the absolute delta
	// can drift wildly via other tests' http servers, postgres clients,
	// etc. Poll for ~1s and accept the smallest delta we observe; the
	// signal is "do my channel's goroutines exit," not "is the test
	// process at a steady-state goroutine count."
	deadline := time.Now().Add(2 * time.Second)
	minDelta := runtime.NumGoroutine() - startGor
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		d := runtime.NumGoroutine() - startGor
		if d < minDelta {
			minDelta = d
		}
		if minDelta <= 8 {
			break
		}
	}
	if minDelta > 8 {
		t.Errorf("goroutines leaked min-delta=%d (start=%d, current=%d)",
			minDelta, startGor, runtime.NumGoroutine())
	}
}
