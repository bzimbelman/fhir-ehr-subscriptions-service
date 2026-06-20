// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package websocket_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	codingws "github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/websocket"
)

// blockingTokens is a websocket.TokenConsumer whose Consume call blocks
// until either the supplied ctx is cancelled or the test signals it to
// release. It records which path unblocked the call so the test can
// assert ctx cancellation came from the channel-scoped context (not the
// http request context).
type blockingTokens struct {
	entered  chan struct{} // closed by Consume on entry
	release  chan struct{} // closed by the test to allow Consume to return
	doneCtx  chan struct{} // closed by Consume if ctx cancellation unblocked it
	subID    uuid.UUID
	clientID string
	expires  time.Time
	now      func() time.Time
}

func newBlockingTokens(subID uuid.UUID, clientID string, now func() time.Time) *blockingTokens {
	return &blockingTokens{
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
		doneCtx:  make(chan struct{}),
		subID:    subID,
		clientID: clientID,
		expires:  now().Add(60 * time.Second),
		now:      now,
	}
}

func (b *blockingTokens) Consume(ctx context.Context, _ string, _ time.Time) (websocket.ConsumeResult, error) {
	close(b.entered)
	select {
	case <-ctx.Done():
		close(b.doneCtx)
		return websocket.ConsumeResult{}, ctx.Err()
	case <-b.release:
		return websocket.ConsumeResult{
			Outcome:        websocket.ConsumeOK,
			SubscriptionID: b.subID,
			ClientID:       b.clientID,
		}, nil
	}
}

// TestRunConnection_CloseCancelsInFlightTokenConsume asserts that a
// channel-wide Close() unblocks an in-flight tokens.Consume call by
// cancelling the context the bind handler passes in. Today (#243) the
// upgrade handler passes r.Context() into runConnection (websocket.go:416),
// which is NOT cancelled by Close() — only by the http handler returning
// or the underlying request being torn down. Phase B fixes this by
// passing c.ctx (or a derived per-connection ctx descended from c.ctx)
// instead, so Close() reliably cancels in-flight bind handshakes that
// have wedged on a slow TokenConsumer.
//
// RED: with r.Context() in upgrade(), Close() returns but the goroutine
// stuck inside Consume continues to wait for `release`. The test fails
// with a timeout reading from blockingTokens.doneCtx.
//
// GREEN: with c.ctx, Close()->c.cancel() propagates into the Consume
// ctx; Consume returns ctx.Err(); doneCtx fires; assertion passes.
func TestRunConnection_CloseCancelsInFlightTokenConsume(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	subID := uuid.New()
	tokens := newBlockingTokens(subID, "client-243", now)

	ch, err := websocket.New(websocket.Options{
		Tokens:   tokens,
		Replayer: newFakeReplayer(),
		Now:      now,
		Metrics:  newFakeMetrics(),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()

	// Drive a real bind frame through the channel so the handler enters
	// runConnection -> tokens.Consume.
	conn := dialClient(t, srv)
	defer conn.Close(codingws.StatusGoingAway, "test cleanup")

	writeBind(t, conn,
		`{"type":"bind","subscriptionId":"`+subID.String()+`","token":"any-token"}`)

	// Wait for the bind handler to enter Consume.
	select {
	case <-tokens.entered:
	case <-time.After(2 * time.Second):
		t.Fatalf("tokens.Consume was not entered within 2s; bind handler stalled")
	}

	// Drain any frames on the client side so server's close handshake can
	// complete (coder/websocket Close waits for peer close frame).
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		for {
			if _, _, rerr := conn.Read(ctx); rerr != nil {
				return
			}
		}
	}()

	// Now Close the channel. After Phase B this MUST cancel the ctx the
	// bind handler holds, releasing Consume with ctx.Err(). Today (RED) it
	// does not, so doneCtx never fires.
	closeDone := make(chan error, 1)
	go func() { closeDone <- ch.Close() }()

	select {
	case <-tokens.doneCtx:
		// GREEN: Consume saw ctx cancellation.
	case <-time.After(2 * time.Second):
		// Release the wedged goroutine so we don't leak it past the test,
		// then fail loudly.
		close(tokens.release)
		<-closeDone
		t.Fatalf("Channel.Close() did not cancel the in-flight tokens.Consume ctx within 2s; " +
			"bind handler is using r.Context() (websocket.go:416) instead of c.ctx — see #243")
	}

	// Sanity: Close completes once the goroutine unwinds.
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("Channel.Close() did not return within 2s after Consume unblocked")
	}
}

// TestRunConnection_CloseCancelsInFlightReplay covers the same defect on
// the replay path. After the bind succeeds inline, runConnection calls
// c.replayer.ReplaySince(ctx, ...) (websocket.go:566). Today ctx is the
// http request context; Close() does not cancel it. Phase B fix: ctx is
// derived from c.ctx, so Close() short-circuits a slow replay store.
func TestRunConnection_CloseCancelsInFlightReplay(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	subID := uuid.New()
	tokens := newFakeTokens(now)
	tokens.Mint("good-token", subID, "client-r", now().Add(60*time.Second))

	replayer := newBlockingReplayer()

	ch, err := websocket.New(websocket.Options{
		Tokens:   tokens,
		Replayer: replayer,
		Now:      now,
		Metrics:  newFakeMetrics(),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()

	conn := dialClient(t, srv)
	defer conn.Close(codingws.StatusGoingAway, "test cleanup")

	// Bind with lastReceivedEventNumber set so runConnection enters the
	// replay branch.
	writeBind(t, conn,
		`{"type":"bind","subscriptionId":"`+subID.String()+
			`","token":"good-token","lastReceivedEventNumber":0}`)

	// Drain the bind-success frame.
	mt, data := readText(t, conn, 2*time.Second)
	if mt != codingws.MessageText || !strings.Contains(string(data), `"bind-success"`) {
		t.Fatalf("expected bind-success, got mt=%v data=%s", mt, data)
	}

	// Wait until the replay handler is wedged in ReplaySince.
	select {
	case <-replayer.entered:
	case <-time.After(2 * time.Second):
		t.Fatalf("ReplaySince was not entered within 2s")
	}

	// Drain any further frames on the client side so the server's
	// close-handshake (sent during Channel.Close -> s.conn.Close) can
	// complete. coder/websocket's Close blocks waiting for the peer
	// close frame; without a reader, that wait stretches Channel.Close
	// to its 5s default.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		for {
			if _, _, rerr := conn.Read(ctx); rerr != nil {
				return
			}
		}
	}()

	closeDone := make(chan error, 1)
	go func() { closeDone <- ch.Close() }()

	select {
	case <-replayer.doneCtx:
		// GREEN
	case <-time.After(2 * time.Second):
		close(replayer.release)
		<-closeDone
		t.Fatalf("Channel.Close() did not cancel the in-flight ReplaySince ctx within 2s; " +
			"runConnection is using r.Context() (websocket.go:416) instead of c.ctx — see #243")
	}

	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("Channel.Close() did not return within 2s")
	}
}

// blockingReplayer is an EventReplayer whose ReplaySince blocks on ctx
// or release, mirroring blockingTokens for the replay path.
type blockingReplayer struct {
	entered chan struct{}
	release chan struct{}
	doneCtx chan struct{}
}

func newBlockingReplayer() *blockingReplayer {
	return &blockingReplayer{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		doneCtx: make(chan struct{}),
	}
}

func (b *blockingReplayer) ReplaySince(ctx context.Context, _ uuid.UUID, _ uint64) ([]websocket.PastEvent, error) {
	close(b.entered)
	select {
	case <-ctx.Done():
		close(b.doneCtx)
		return nil, ctx.Err()
	case <-b.release:
		return nil, nil
	}
}
