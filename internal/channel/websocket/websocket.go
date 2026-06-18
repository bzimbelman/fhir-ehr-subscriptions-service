// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package websocket implements the FHIR R5 WebSocket notification
// channel. The channel runs a server-side WSS endpoint subscribers connect
// to after acquiring a single-use binding token via $get-ws-binding-token,
// then writes subscription-notification Bundle frames over the socket.
//
// See docs/low-level-design/channels.md §4.2 for the contract.
package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	codingws "github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
)

// Channel name constant; used in metric labels and span names.
const channelName = "websocket"

// HandlerPath is the URL path the channel registers its upgrade handler at.
const HandlerPath = "/ws/subscriptions"

// Defaults from docs/low-level-design/channels.md §4.2.
const (
	DefaultPingInterval        = 30 * time.Second
	DefaultIdleTimeout         = 5 * time.Minute
	DefaultMaxFrameBytes       = 8 * 1024 * 1024
	DefaultTransientRetryAfter = 30 * time.Second
	DefaultAckTimeout          = 30 * time.Second
)

// Metric names.
const (
	MetricDeliveriesTotal     = "fhir_subs_channel_websocket_deliveries_total"
	MetricBindAttemptsTotal   = "fhir_subs_channel_websocket_bind_attempts_total"
	MetricSessionsBoundGauge  = "fhir_subs_channel_websocket_sessions_bound"
	MetricFrameBytesHistogram = "fhir_subs_channel_websocket_frame_bytes"
	MetricDisconnectsTotal    = "fhir_subs_channel_websocket_disconnects_total"
	MetricReplayEventsTotal   = "fhir_subs_channel_websocket_replay_events_total"
)

// ConsumeOutcome and ConsumeResult mirror the storage repo's enum so the
// websocket package can take a small interface dependency rather than a
// concrete repo type.

// ConsumeOutcome reports the result of a single-use token redemption.
type ConsumeOutcome int

// ConsumeOutcome values.
const (
	// ConsumeOK means the token was redeemed and the bound subscription is
	// returned in ConsumeResult.SubscriptionID.
	ConsumeOK ConsumeOutcome = iota
	// ConsumeNotFound means no row exists for the supplied token.
	ConsumeNotFound
	// ConsumeAlreadyUsed means the token has been redeemed previously.
	ConsumeAlreadyUsed
	// ConsumeExpired means the token's expires_at is in the past.
	ConsumeExpired
)

// ConsumeResult is the typed result of a Consume call.
type ConsumeResult struct {
	Outcome        ConsumeOutcome
	SubscriptionID uuid.UUID
	ClientID       string
}

// TokenConsumer is the seam to the ws_binding_tokens repository.
type TokenConsumer interface {
	Consume(ctx context.Context, token string, now time.Time) (ConsumeResult, error)
}

// PastEvent is one previously-emitted notification a subscriber missed
// while disconnected.
type PastEvent struct {
	EventNumber uint64
	Bundle      []byte
	ContentType channel.ContentType
}

// EventReplayer fetches missed events for replay on reconnect.
type EventReplayer interface {
	ReplaySince(ctx context.Context, subscriptionID uuid.UUID, after uint64) ([]PastEvent, error)
}

// Options configures a Channel.
type Options struct {
	Tokens              TokenConsumer
	Replayer            EventReplayer
	Now                 func() time.Time
	Metrics             channel.MetricsEmitter
	Logger              *slog.Logger
	PingInterval        time.Duration
	IdleTimeout         time.Duration
	MaxFrameBytes       int
	TransientRetryAfter time.Duration
	AckTimeout          time.Duration
}

// Channel is the websocket notification channel.
type Channel struct {
	tokens              TokenConsumer
	replayer            EventReplayer
	now                 func() time.Time
	metrics             channel.MetricsEmitter
	logger              *slog.Logger
	pingInterval        time.Duration
	idleTimeout         time.Duration
	maxFrameBytes       int
	transientRetryAfter time.Duration
	ackTimeout          time.Duration

	// sessions holds the at-most-one bound session per subscription.
	mu       sync.Mutex
	sessions map[uuid.UUID]*session
	closed   bool
}

// session is the per-subscription connection state.
type session struct {
	subID    uuid.UUID
	clientID string
	conn     *codingws.Conn

	// sendMu serializes outbound frames so eventNumber order is preserved.
	sendMu sync.Mutex

	// ackMu protects the ack waiters map.
	ackMu    sync.Mutex
	ackWaits map[uint64]chan struct{}

	// closing is set when the read loop has terminated.
	closing chan struct{}

	logger *slog.Logger
}

// New constructs a Channel. Tokens and Replayer are required.
func New(opts Options) (*Channel, error) {
	if opts.Tokens == nil {
		return nil, errors.New("websocket: Tokens is required")
	}
	if opts.Replayer == nil {
		return nil, errors.New("websocket: Replayer is required")
	}
	c := &Channel{
		tokens:              opts.Tokens,
		replayer:            opts.Replayer,
		now:                 opts.Now,
		metrics:             opts.Metrics,
		logger:              opts.Logger,
		pingInterval:        opts.PingInterval,
		idleTimeout:         opts.IdleTimeout,
		maxFrameBytes:       opts.MaxFrameBytes,
		transientRetryAfter: opts.TransientRetryAfter,
		ackTimeout:          opts.AckTimeout,
		sessions:            make(map[uuid.UUID]*session),
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.metrics == nil {
		c.metrics = channel.NopMetrics{}
	}
	if c.logger == nil {
		c.logger = slog.Default()
	}
	if c.pingInterval <= 0 {
		c.pingInterval = DefaultPingInterval
	}
	if c.idleTimeout <= 0 {
		c.idleTimeout = DefaultIdleTimeout
	}
	if c.maxFrameBytes <= 0 {
		c.maxFrameBytes = DefaultMaxFrameBytes
	}
	if c.transientRetryAfter <= 0 {
		c.transientRetryAfter = DefaultTransientRetryAfter
	}
	if c.ackTimeout <= 0 {
		c.ackTimeout = DefaultAckTimeout
	}
	return c, nil
}

// Handler returns the http.Handler that accepts WSS upgrade requests at
// /ws/subscriptions and binds the connection to a subscription via the
// bind handshake.
func (c *Channel) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(HandlerPath, c.upgrade)
	return mux
}

// HandleUpgrade is exported for hosts that want to register the upgrade
// handler at a path other than HandlerPath (e.g., behind a base path).
func (c *Channel) HandleUpgrade(w http.ResponseWriter, r *http.Request) {
	c.upgrade(w, r)
}

func (c *Channel) upgrade(w http.ResponseWriter, r *http.Request) {
	conn, err := codingws.Accept(w, r, &codingws.AcceptOptions{
		// InsecureSkipVerify is fine for tests using httptest with a
		// matching Origin; for production deployments, operators set
		// allowed origins via the host's TLS-terminating reverse proxy.
		InsecureSkipVerify: true,
	})
	if err != nil {
		c.logger.WarnContext(r.Context(), "websocket accept failed",
			slog.String("channel", channelName),
			slog.String("err", err.Error()))
		return
	}

	c.runConnection(r.Context(), conn)
}

// runConnection reads the bind message, validates the token, registers
// the session, optionally replays missed events, then runs the read
// loop until the socket closes.
func (c *Channel) runConnection(ctx context.Context, conn *codingws.Conn) {
	// Bind message has its own short read deadline.
	bindCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	mt, raw, err := conn.Read(bindCtx)
	cancel()
	if err != nil {
		_ = conn.Close(codingws.StatusPolicyViolation, "no bind message")
		return
	}
	if mt != codingws.MessageText {
		_ = conn.Write(ctx, codingws.MessageText, mustJSON(bindError("non-text bind message")))
		_ = conn.Close(codingws.StatusUnsupportedData, "bind must be text")
		return
	}

	var msg struct {
		Type                    string `json:"type"`
		SubscriptionID          string `json:"subscriptionId"`
		Token                   string `json:"token"`
		LastReceivedEventNumber *int64 `json:"lastReceivedEventNumber"`
	}
	if uerr := json.Unmarshal(raw, &msg); uerr != nil {
		_ = conn.Write(ctx, codingws.MessageText, mustJSON(bindError("invalid bind json: "+uerr.Error())))
		_ = conn.Close(codingws.StatusUnsupportedData, "invalid bind")
		return
	}
	if msg.Type != "bind" {
		_ = conn.Write(ctx, codingws.MessageText, mustJSON(bindError("unexpected message type: "+msg.Type)))
		_ = conn.Close(codingws.StatusPolicyViolation, "expected bind")
		return
	}

	claimed, err := uuid.Parse(msg.SubscriptionID)
	if err != nil {
		_ = conn.Write(ctx, codingws.MessageText, mustJSON(bindError("invalid subscriptionId")))
		_ = conn.Close(codingws.StatusPolicyViolation, "invalid subscriptionId")
		return
	}

	c.metrics.Inc(MetricBindAttemptsTotal, map[string]string{"channel": channelName})

	res, err := c.tokens.Consume(ctx, msg.Token, c.now())
	if err != nil {
		c.logger.ErrorContext(ctx, "token consume error",
			slog.String("channel", channelName),
			slog.String("err", err.Error()))
		_ = conn.Write(ctx, codingws.MessageText, mustJSON(bindError("token store error")))
		_ = conn.Close(codingws.StatusInternalError, "token error")
		return
	}
	if res.Outcome != ConsumeOK {
		_ = conn.Write(ctx, codingws.MessageText, mustJSON(bindError(consumeReason(res.Outcome))))
		_ = conn.Close(codingws.StatusPolicyViolation, "bind rejected")
		return
	}
	if res.SubscriptionID != claimed {
		_ = conn.Write(ctx, codingws.MessageText, mustJSON(bindError("token does not match subscription")))
		_ = conn.Close(codingws.StatusPolicyViolation, "subscription mismatch")
		return
	}

	// Register the session; reject if another connection already holds the
	// subscription.
	sess := &session{
		subID:    res.SubscriptionID,
		clientID: res.ClientID,
		conn:     conn,
		ackWaits: make(map[uint64]chan struct{}),
		closing:  make(chan struct{}),
		logger:   c.logger.With(slog.String("subscription_id", res.SubscriptionID.String())),
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = conn.Close(codingws.StatusGoingAway, "channel closed")
		return
	}
	if _, exists := c.sessions[res.SubscriptionID]; exists {
		c.mu.Unlock()
		_ = conn.Write(ctx, codingws.MessageText, mustJSON(bindError("subscription already bound")))
		_ = conn.Close(codingws.StatusPolicyViolation, "already bound")
		return
	}
	c.sessions[res.SubscriptionID] = sess
	c.metrics.Set(MetricSessionsBoundGauge, float64(len(c.sessions)), map[string]string{"channel": channelName})
	c.mu.Unlock()

	// Send bind-success.
	if err := writeText(ctx, conn, mustJSON(map[string]any{
		"type":           "bind-success",
		"subscriptionId": res.SubscriptionID.String(),
	})); err != nil {
		c.removeSession(sess)
		_ = conn.Close(codingws.StatusInternalError, "write bind-success failed")
		return
	}

	// Replay missed events if the subscriber asked for it.
	if msg.LastReceivedEventNumber != nil && *msg.LastReceivedEventNumber >= 0 {
		past, err := c.replayer.ReplaySince(ctx, res.SubscriptionID, uint64(*msg.LastReceivedEventNumber))
		if err != nil {
			sess.logger.ErrorContext(ctx, "replay error", slog.String("err", err.Error()))
		} else {
			for _, e := range past {
				sess.sendMu.Lock()
				werr := writeText(ctx, conn, e.Bundle)
				sess.sendMu.Unlock()
				if werr != nil {
					sess.logger.ErrorContext(ctx, "replay write failed", slog.String("err", werr.Error()))
					break
				}
				c.metrics.Inc(MetricReplayEventsTotal, map[string]string{"channel": channelName})
			}
		}
	}

	// Pong on every read keeps last_activity fresh; coder/websocket auto-replies
	// to client pings, but we also drive periodic server pings to detect dead
	// peers.
	go c.pingLoop(sess)

	c.readLoop(ctx, sess)
	c.removeSession(sess)
	_ = conn.Close(codingws.StatusNormalClosure, "")
}

func (c *Channel) readLoop(ctx context.Context, sess *session) {
	defer close(sess.closing)
	for {
		mt, data, err := sess.conn.Read(ctx)
		if err != nil {
			c.metrics.Inc(MetricDisconnectsTotal, map[string]string{
				"channel": channelName,
				"reason":  classifyClose(err),
			})
			return
		}
		if mt != codingws.MessageText {
			continue
		}
		var msg struct {
			Type        string `json:"type"`
			EventNumber uint64 `json:"eventNumber"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "ack":
			sess.deliverAck(msg.EventNumber)
		default:
			// Ignore unrecognized client message types after bind; the
			// LLD does not require active error reporting on each frame.
		}
	}
}

func (c *Channel) pingLoop(sess *session) {
	tick := time.NewTicker(c.pingInterval)
	defer tick.Stop()
	for {
		select {
		case <-sess.closing:
			return
		case <-tick.C:
			ctx, cancel := context.WithTimeout(context.Background(), c.idleTimeout)
			err := sess.conn.Ping(ctx)
			cancel()
			if err != nil {
				sess.logger.WarnContext(context.Background(), "ping failed", slog.String("err", err.Error()))
				return
			}
		}
	}
}

func (c *Channel) removeSession(sess *session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cur, ok := c.sessions[sess.subID]; ok && cur == sess {
		delete(c.sessions, sess.subID)
		c.metrics.Set(MetricSessionsBoundGauge, float64(len(c.sessions)), map[string]string{"channel": channelName})
	}
}

// Deliver implements channel.Channel.
func (c *Channel) Deliver(ctx context.Context, env channel.NotificationEnvelope) (channel.DeliveryOutcome, error) {
	c.mu.Lock()
	sess, ok := c.sessions[env.SubscriptionID]
	c.mu.Unlock()
	if !ok {
		c.observeOutcome(channel.OutcomeTransient)
		return channel.TransientFailure(c.transientRetryAfter, "no socket"), nil
	}

	if c.maxFrameBytes > 0 && len(env.BundleBytes) > c.maxFrameBytes {
		c.observeOutcome(channel.OutcomePermanent)
		return channel.PermanentFailure(fmt.Sprintf(
			"frame size %d exceeds max_frame_bytes %d",
			len(env.BundleBytes), c.maxFrameBytes)), nil
	}

	// Heartbeats and other zero-sequence bundles do not require an ack.
	expectAck := env.BundleKind == channel.BundleEventNotification

	// Register the ack waiter BEFORE the write so we cannot miss an ack
	// that arrives before we begin waiting.
	var ackCh chan struct{}
	if expectAck {
		ackCh = sess.registerAck(env.Sequence)
		defer sess.cancelAck(env.Sequence)
	}

	sess.sendMu.Lock()
	writeCtx, writeCancel := c.attemptContext(ctx, env.Deadline)
	werr := writeText(writeCtx, sess.conn, env.BundleBytes)
	writeCancel()
	sess.sendMu.Unlock()

	if werr != nil {
		if errors.Is(werr, context.DeadlineExceeded) || errors.Is(werr, context.Canceled) {
			c.observeOutcome(channel.OutcomeTransient)
			return channel.TransientFailure(0, "write deadline: "+werr.Error()), nil
		}
		c.observeOutcome(channel.OutcomeTransient)
		return channel.TransientFailure(0, "write: "+werr.Error()), nil
	}
	c.metrics.Observe(MetricFrameBytesHistogram, float64(len(env.BundleBytes)),
		map[string]string{"channel": channelName})

	if !expectAck {
		c.observeOutcome(channel.OutcomeDelivered)
		return channel.Delivered(), nil
	}

	// Wait for the matching ack. The ack timeout is bounded by both the
	// envelope's deadline and the channel's AckTimeout floor.
	ackCtx, cancel := c.ackContext(ctx, env.Deadline)
	defer cancel()
	select {
	case <-ackCh:
		c.observeOutcome(channel.OutcomeDelivered)
		return channel.Delivered(), nil
	case <-sess.closing:
		c.observeOutcome(channel.OutcomeTransient)
		return channel.TransientFailure(c.transientRetryAfter, "socket closed before ack"), nil
	case <-ackCtx.Done():
		c.observeOutcome(channel.OutcomeTransient)
		return channel.TransientFailure(0, "ack timeout"), nil
	}
}

// Close stops the channel and disconnects bound subscribers.
func (c *Channel) Close() error {
	c.mu.Lock()
	c.closed = true
	sess := make([]*session, 0, len(c.sessions))
	for _, s := range c.sessions {
		sess = append(sess, s)
	}
	c.sessions = map[uuid.UUID]*session{}
	c.mu.Unlock()
	for _, s := range sess {
		_ = s.conn.Close(codingws.StatusGoingAway, "channel shutting down")
	}
	return nil
}

// attemptContext derives a context bounded by the envelope deadline (if any).
func (c *Channel) attemptContext(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	if !deadline.IsZero() {
		return context.WithDeadline(parent, deadline)
	}
	return context.WithTimeout(parent, c.ackTimeout)
}

// ackContext derives the wait-for-ack context, bounded by the smaller of
// the envelope deadline and the channel's ack timeout.
func (c *Channel) ackContext(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	timeout := c.ackTimeout
	if !deadline.IsZero() {
		if d := time.Until(deadline); d > 0 && d < timeout {
			timeout = d
		}
	}
	return context.WithTimeout(parent, timeout)
}

func (c *Channel) observeOutcome(k channel.OutcomeKind) {
	c.metrics.Inc(MetricDeliveriesTotal, map[string]string{
		"channel": channelName,
		"outcome": outcomeLabel(k),
	})
}

// session methods.

func (s *session) registerAck(eventNumber uint64) chan struct{} {
	ch := make(chan struct{})
	s.ackMu.Lock()
	s.ackWaits[eventNumber] = ch
	s.ackMu.Unlock()
	return ch
}

func (s *session) cancelAck(eventNumber uint64) {
	s.ackMu.Lock()
	delete(s.ackWaits, eventNumber)
	s.ackMu.Unlock()
}

func (s *session) deliverAck(eventNumber uint64) {
	s.ackMu.Lock()
	ch, ok := s.ackWaits[eventNumber]
	if ok {
		delete(s.ackWaits, eventNumber)
	}
	s.ackMu.Unlock()
	if ok {
		close(ch)
	}
}

// helpers.

func writeText(ctx context.Context, conn *codingws.Conn, body []byte) error {
	return conn.Write(ctx, codingws.MessageText, body)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		// Fall back to a minimal valid JSON envelope; encoding errors here
		// are bugs in this package, not runtime conditions.
		return []byte(`{"type":"bind-error","reason":"internal encoding error"}`)
	}
	return b
}

func bindError(reason string) map[string]any {
	return map[string]any{
		"type":   "bind-error",
		"reason": reason,
	}
}

func consumeReason(o ConsumeOutcome) string {
	switch o {
	case ConsumeNotFound:
		return "token not found"
	case ConsumeAlreadyUsed:
		return "token already consumed"
	case ConsumeExpired:
		return "token expired"
	default:
		return "token rejected"
	}
}

func outcomeLabel(k channel.OutcomeKind) string {
	switch k {
	case channel.OutcomeDelivered:
		return "delivered"
	case channel.OutcomeTransient:
		return "transient"
	case channel.OutcomePermanent:
		return "permanent"
	default:
		return "unknown"
	}
}

func classifyClose(err error) string {
	if err == nil {
		return "ok"
	}
	cs := codingws.CloseStatus(err)
	if cs != -1 {
		return fmt.Sprintf("close-%d", int(cs))
	}
	return "io"
}
