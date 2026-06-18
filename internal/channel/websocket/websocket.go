// Copyright the fhir-ehr-subscriptions-service authors.
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
	"sync/atomic"
	"time"

	codingws "github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
)

// Channel name constant; used in metric labels and span names.
const channelName = "websocket"

// HandlerPath is the URL path the channel registers its upgrade handler at.
const HandlerPath = "/ws/subscriptions"

// SubprotocolBindPrefix is the well-known Sec-WebSocket-Protocol prefix
// the spec settled on for the FHIR R5 WebSocket Subscription transport
// (P1.9). A subscriber that wants to deliver the bind token via the
// upgrade handshake (rather than the in-band JSON form) sets the
// header to:
//
//	Sec-WebSocket-Protocol: fhirsubscriptions.v1.<base64url-token>
//
// The server negotiates back the same value; the bind frame then omits
// the "token" field and the server uses the header-derived token.
// Backward compatibility with the in-band JSON form is preserved.
const SubprotocolBindPrefix = "fhirsubscriptions.v1."

// Defaults from docs/low-level-design/channels.md §4.2.
const (
	DefaultPingInterval             = 30 * time.Second
	DefaultIdleTimeout              = 5 * time.Minute
	DefaultMaxFrameBytes            = 8 * 1024 * 1024
	DefaultTransientRetryAfter      = 30 * time.Second
	DefaultAckTimeout               = 30 * time.Second
	DefaultBindTimeout              = 10 * time.Second
	DefaultPingWriteTimeout         = 10 * time.Second
	DefaultUpgradeReadHeaderTimeout = 5 * time.Second
	// DefaultMaxReplayEvents bounds replay materialization to a sane upper
	// limit; clients reconnecting from an ancient lastReceivedEventNumber
	// receive at most this many events plus a "replay-truncated" control
	// frame instructing them to bind again with the latest event number.
	DefaultMaxReplayEvents = 10000
	// DefaultMaxSessions bounds the channel-wide session map; a single
	// process holding millions of bound sockets is operationally untenable.
	DefaultMaxSessions = 50000
)

// Metric names.
const (
	MetricDeliveriesTotal      = "fhir_subs_channel_websocket_deliveries_total"
	MetricBindAttemptsTotal    = "fhir_subs_channel_websocket_bind_attempts_total"
	MetricSessionsBoundGauge   = "fhir_subs_channel_websocket_sessions_bound"
	MetricFrameBytesHistogram  = "fhir_subs_channel_websocket_frame_bytes"
	MetricDisconnectsTotal     = "fhir_subs_channel_websocket_disconnects_total"
	MetricReplayEventsTotal    = "fhir_subs_channel_websocket_replay_events_total"
	MetricBindRejectedTotal    = "fhir_subs_channel_websocket_bind_rejected_total"
	MetricReplayTruncatedTotal = "fhir_subs_channel_websocket_replay_truncated_total"
	MetricIdleClosedTotal      = "fhir_subs_channel_websocket_idle_closed_total"
	// MetricUnknownAckTotal counts ack frames whose eventNumber is not
	// in the sent-set. A misbehaving or hostile client gets visibility
	// here; a healthy client should never increment it (N-1).
	MetricUnknownAckTotal = "fhir_subs_channel_websocket_unknown_ack_total"
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

	// OriginPatterns is the allow-list of host patterns the upgrade
	// handler accepts in the WebSocket handshake's Origin header (B-17).
	// Patterns follow the coder/websocket AcceptOptions.OriginPatterns
	// matcher: a host glob like "trusted.example" or "*.example.com".
	// Empty / nil means "same-origin only" — cross-origin upgrades are
	// rejected with HTTP 403. To opt into accepting upgrades from a
	// browser hosted on a different domain, list its host explicitly.
	// Reverse-proxy deployments behind TLS termination should set this
	// to the public host(s) the browser sees, not the upstream service.
	OriginPatterns []string

	// BindTimeout bounds the wait for the first bind frame after upgrade.
	// Zero falls back to DefaultBindTimeout. (S-7 #4)
	BindTimeout time.Duration

	// PingWriteTimeout bounds a single ping write so a stuck peer cannot
	// pin a goroutine for the full IdleTimeout. Zero falls back to
	// DefaultPingWriteTimeout. (S-7 #6)
	PingWriteTimeout time.Duration

	// UpgradeReadHeaderTimeout, when wired via ConfigureServer, is set as
	// the http.Server.ReadHeaderTimeout to defend the upgrade handler
	// against slowloris on the handshake. Zero falls back to
	// DefaultUpgradeReadHeaderTimeout. (S-7 #2)
	UpgradeReadHeaderTimeout time.Duration

	// MaxSessions bounds the channel-wide session map. Bind requests
	// beyond this cap are rejected with bind-error and a metric increment
	// on fhir_subs_channel_websocket_bind_rejected_total{reason="capacity"}.
	// Zero falls back to DefaultMaxSessions. (S-7 #1)
	MaxSessions int

	// MaxSessionsPerClient limits concurrent bound sessions per ClientID.
	// Zero disables the per-client cap (only the channel-wide MaxSessions
	// applies). (S-7 #1)
	MaxSessionsPerClient int

	// MaxReplayEvents caps the number of past events written during the
	// reconnect-replay step. After the cap the channel emits a
	// "replay-truncated" control frame and stops; the client should
	// re-bind with the latest received event number. Zero falls back to
	// DefaultMaxReplayEvents. (S-7 #8)
	MaxReplayEvents int
}

// Channel is the websocket notification channel.
type Channel struct {
	tokens                   TokenConsumer
	replayer                 EventReplayer
	now                      func() time.Time
	metrics                  channel.MetricsEmitter
	logger                   *slog.Logger
	pingInterval             time.Duration
	idleTimeout              time.Duration
	maxFrameBytes            int
	transientRetryAfter      time.Duration
	ackTimeout               time.Duration
	originPatterns           []string
	bindTimeout              time.Duration
	pingWriteTimeout         time.Duration
	upgradeReadHeaderTimeout time.Duration
	maxSessions              int
	maxSessionsPerClient     int
	maxReplayEvents          int

	// ctx / cancel govern channel-wide goroutine lifecycle (S-7 #5).
	ctx    context.Context
	cancel context.CancelFunc
	// wg tracks per-session goroutines (read + ping loops) so Close can
	// join cleanly (S-7 #9).
	wg sync.WaitGroup

	// sessions holds the at-most-one bound session per subscription.
	mu              sync.Mutex
	sessions        map[uuid.UUID]*session
	clientSessCount map[string]int
	closed          bool
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
	ackWaits map[uint64]*ackWaiter

	// closing is set when the read loop has terminated.
	closing chan struct{}

	// lastReadAtNS is the most recent time, in unix nanoseconds, the
	// session observed a frame from the peer. Updated atomically by
	// readLoop; the idle watchdog reads it without taking sendMu.
	// (S-7 #6/#7)
	lastReadAtNS atomic.Int64

	logger *slog.Logger
}

// ackWaiter wraps the per-sequence ack channel with a sync.Once so the
// channel is closed exactly once even when multiple paths race to close
// it (B-18). Before this, deliverAck and any timed-out Deliver path
// could each call close on the same channel under concurrent ack
// arrival, panicking the goroutine.
type ackWaiter struct {
	ch   chan struct{}
	once sync.Once
}

func (a *ackWaiter) closeOnce() {
	a.once.Do(func() { close(a.ch) })
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
		tokens:                   opts.Tokens,
		replayer:                 opts.Replayer,
		now:                      opts.Now,
		metrics:                  opts.Metrics,
		logger:                   opts.Logger,
		pingInterval:             opts.PingInterval,
		idleTimeout:              opts.IdleTimeout,
		maxFrameBytes:            opts.MaxFrameBytes,
		transientRetryAfter:      opts.TransientRetryAfter,
		ackTimeout:               opts.AckTimeout,
		originPatterns:           append([]string(nil), opts.OriginPatterns...),
		bindTimeout:              opts.BindTimeout,
		pingWriteTimeout:         opts.PingWriteTimeout,
		upgradeReadHeaderTimeout: opts.UpgradeReadHeaderTimeout,
		maxSessions:              opts.MaxSessions,
		maxSessionsPerClient:     opts.MaxSessionsPerClient,
		maxReplayEvents:          opts.MaxReplayEvents,
		sessions:                 make(map[uuid.UUID]*session),
		clientSessCount:          make(map[string]int),
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
	if c.bindTimeout <= 0 {
		c.bindTimeout = DefaultBindTimeout
	}
	if c.pingWriteTimeout <= 0 {
		c.pingWriteTimeout = DefaultPingWriteTimeout
	}
	if c.upgradeReadHeaderTimeout <= 0 {
		c.upgradeReadHeaderTimeout = DefaultUpgradeReadHeaderTimeout
	}
	if c.maxSessions <= 0 {
		c.maxSessions = DefaultMaxSessions
	}
	if c.maxReplayEvents <= 0 {
		c.maxReplayEvents = DefaultMaxReplayEvents
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	return c, nil
}

// ConfigureServer applies the upgrade handshake hardening to a passed
// *http.Server: ReadHeaderTimeout (S-7 #2) so a slowloris on the
// handshake gets cut off promptly. The caller is responsible for setting
// the Handler. This is a hook because we don't own the server lifecycle
// — the host wires the channel handler into a larger mux.
func (c *Channel) ConfigureServer(s *http.Server) {
	if s == nil {
		return
	}
	if s.ReadHeaderTimeout == 0 || s.ReadHeaderTimeout > c.upgradeReadHeaderTimeout {
		s.ReadHeaderTimeout = c.upgradeReadHeaderTimeout
	}
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
	// P1.9: subscribers that follow the spec's recommended transport
	// (Sec-WebSocket-Protocol header carrying the bind token) advertise
	// a subprotocol of the form "fhirsubscriptions.v1.<token>". We
	// extract the offered subprotocols, find any that match the prefix,
	// and pass them to AcceptOptions.Subprotocols so the negotiation
	// honors the client's choice. The bind handler reads the negotiated
	// protocol back via conn.Subprotocol() and uses the embedded token
	// in lieu of the in-band JSON token field.
	offered := r.Header.Values("Sec-WebSocket-Protocol")
	var allow []string
	for _, header := range offered {
		// Header may carry comma-separated values per RFC 6455; the
		// underlying library splits per call to Header.Values when each
		// is supplied separately, so accept both shapes.
		for _, p := range splitProtocolList(header) {
			if len(p) > len(SubprotocolBindPrefix) && p[:len(SubprotocolBindPrefix)] == SubprotocolBindPrefix {
				allow = append(allow, p)
			}
		}
	}

	// B-17: enforce Origin checking. Default-deny cross-origin
	// upgrades; operators opt in via Options.OriginPatterns. The
	// underlying coder/websocket library rejects with HTTP 403 when an
	// upgrade request presents an Origin not on the list AND the host
	// does not match. Reverse-proxy deployments do NOT check Origin —
	// the application must.
	conn, err := codingws.Accept(w, r, &codingws.AcceptOptions{
		OriginPatterns: c.originPatterns,
		Subprotocols:   allow,
	})
	if err != nil {
		c.logger.WarnContext(r.Context(), "websocket accept failed",
			slog.String("channel", channelName),
			slog.String("err", err.Error()),
			slog.String("origin", r.Header.Get("Origin")))
		return
	}

	// S-7 #3: align inbound read limit with the channel's MaxFrameBytes.
	// Default coder/websocket inbound limit is 32 KiB; without this call,
	// any inbound frame above 32 KiB closes the connection even though
	// the operator configured MaxFrameBytes=8MB outbound. We use the
	// larger of MaxFrameBytes and the bind-message default so an operator
	// who configures a deliberately tiny MaxFrameBytes (rare; mainly
	// tests) does not accidentally make the bind frame unsendable. The
	// post-bind tightening is applied inside runConnection.
	bindReadLimit := int64(c.maxFrameBytes)
	if bindReadLimit < defaultBindReadLimit {
		bindReadLimit = defaultBindReadLimit
	}
	conn.SetReadLimit(bindReadLimit)

	c.runConnection(r.Context(), conn)
}

// defaultBindReadLimit is the inbound read limit applied during the
// bind handshake. It is intentionally generous (4 KiB) — the bind frame
// is small JSON, and we want any larger inbound frame to be rejected
// before we trust the peer.
const defaultBindReadLimit = 4 * 1024

// runConnection reads the bind message, validates the token, registers
// the session, optionally replays missed events, then runs the read
// loop until the socket closes.
func (c *Channel) runConnection(ctx context.Context, conn *codingws.Conn) {
	// S-7 #4: bind-frame timeout is configurable via Options.BindTimeout.
	bindCtx, cancel := context.WithTimeout(ctx, c.bindTimeout)
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

	// P1.9: if the upgrade negotiated the fhirsubscriptions.v1.<token>
	// subprotocol, prefer the header-derived token over any in-band one
	// the bind frame happened to carry. The frame still owns the
	// subscriptionId (and optional lastReceivedEventNumber).
	if proto := conn.Subprotocol(); len(proto) > len(SubprotocolBindPrefix) && proto[:len(SubprotocolBindPrefix)] == SubprotocolBindPrefix {
		msg.Token = proto[len(SubprotocolBindPrefix):]
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
		ackWaits: make(map[uint64]*ackWaiter),
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
	// S-7 #1: channel-wide session cap.
	if len(c.sessions) >= c.maxSessions {
		c.mu.Unlock()
		c.metrics.Inc(MetricBindRejectedTotal, map[string]string{
			"channel": channelName,
			"reason":  "capacity",
		})
		_ = conn.Write(ctx, codingws.MessageText, mustJSON(bindError("max sessions capacity reached")))
		_ = conn.Close(codingws.StatusTryAgainLater, "capacity")
		return
	}
	// S-7 #1: per-client session cap.
	if c.maxSessionsPerClient > 0 && c.clientSessCount[res.ClientID] >= c.maxSessionsPerClient {
		c.mu.Unlock()
		c.metrics.Inc(MetricBindRejectedTotal, map[string]string{
			"channel": channelName,
			"reason":  "client_capacity",
		})
		_ = conn.Write(ctx, codingws.MessageText, mustJSON(bindError("max sessions per client capacity reached")))
		_ = conn.Close(codingws.StatusTryAgainLater, "client capacity")
		return
	}
	c.sessions[res.SubscriptionID] = sess
	c.clientSessCount[res.ClientID]++
	sess.lastReadAtNS.Store(c.now().UnixNano())
	c.metrics.Set(MetricSessionsBoundGauge, float64(len(c.sessions)), map[string]string{"channel": channelName})
	c.mu.Unlock()

	// S-7 #3: post-bind, tighten the inbound read limit to MaxFrameBytes.
	// During bind we used a generous default to avoid rejecting the bind
	// frame on a deliberately tiny MaxFrameBytes (rare). After bind, the
	// channel's outbound limit applies inbound too.
	conn.SetReadLimit(int64(c.maxFrameBytes))

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
			// S-7 #8: cap the replay so a million-event subscription does
			// not OOM the channel. After the cap the channel emits a
			// "replay-truncated" control message and stops; the client
			// should re-bind with the latest received event number.
			limit := len(past)
			truncated := false
			if c.maxReplayEvents > 0 && limit > c.maxReplayEvents {
				limit = c.maxReplayEvents
				truncated = true
			}
			for i := 0; i < limit; i++ {
				e := past[i]
				sess.sendMu.Lock()
				werr := writeText(ctx, conn, e.Bundle)
				sess.sendMu.Unlock()
				if werr != nil {
					sess.logger.ErrorContext(ctx, "replay write failed", slog.String("err", werr.Error()))
					break
				}
				c.metrics.Inc(MetricReplayEventsTotal, map[string]string{"channel": channelName})
			}
			if truncated {
				c.metrics.Inc(MetricReplayTruncatedTotal, map[string]string{"channel": channelName})
				sess.sendMu.Lock()
				_ = writeText(ctx, conn, mustJSON(map[string]any{
					"type":    "replay-truncated",
					"reason":  "replay capped at max-replay-events; rebind with the latest received event",
					"capped":  c.maxReplayEvents,
					"missing": len(past) - limit,
				}))
				sess.sendMu.Unlock()
			}
		}
	}

	// S-7 #5/#9: spawn the per-session goroutines under the channel
	// WaitGroup so Close can join them deterministically. Both loops are
	// bound to c.ctx so a Close fires their cancellation simultaneously.
	c.wg.Add(2)
	go func() {
		defer c.wg.Done()
		c.pingLoop(sess)
	}()
	go func() {
		defer c.wg.Done()
		c.readLoop(c.ctx, sess)
		c.removeSession(sess)
		_ = conn.Close(codingws.StatusNormalClosure, "")
	}()
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
		// S-7 #6/#7: any peer frame refreshes the idle watermark so the
		// pingLoop's idle watchdog tracks "last seen activity" instead of
		// piggybacking ping success.
		sess.lastReadAtNS.Store(c.now().UnixNano())
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
			// N-1: validate the ack against the in-flight sent-set.
			// Unknown event numbers are silently dropped today; emit a
			// metric so a hostile client is observable.
			if !sess.deliverAck(msg.EventNumber) {
				c.metrics.Inc(MetricUnknownAckTotal, map[string]string{"channel": channelName})
			}
		default:
			// Ignore unrecognized client message types after bind; the
			// LLD does not require active error reporting on each frame.
		}
	}
}

// pingLoop drives periodic pings AND polices idle timeout. S-7 #5: the
// inner ping context is bound to the channel-level ctx so a Close
// cancels in-flight pings. S-7 #6: the ping write deadline is the
// dedicated PingWriteTimeout, NOT the full IdleTimeout. S-7 #7: idle
// detection uses the session's lastReadAtNS watermark; a session that
// has been quiet for IdleTimeout is closed.
func (c *Channel) pingLoop(sess *session) {
	tick := time.NewTicker(c.pingInterval)
	defer tick.Stop()
	for {
		select {
		case <-sess.closing:
			return
		case <-c.ctx.Done():
			return
		case <-tick.C:
			// Idle watchdog (S-7 #7).
			lastNS := sess.lastReadAtNS.Load()
			if lastNS > 0 {
				if c.now().UnixNano()-lastNS > int64(c.idleTimeout) {
					sess.logger.InfoContext(c.ctx, "websocket idle timeout reached; closing session",
						slog.String("channel", channelName),
						slog.Duration("idle_timeout", c.idleTimeout))
					c.metrics.Inc(MetricIdleClosedTotal, map[string]string{"channel": channelName})
					_ = sess.conn.Close(codingws.StatusGoingAway, "idle timeout")
					return
				}
			}
			// Bounded ping (S-7 #5/#6).
			pingCtx, cancel := context.WithTimeout(c.ctx, c.pingWriteTimeout)
			err := sess.conn.Ping(pingCtx)
			cancel()
			if err != nil {
				sess.logger.WarnContext(c.ctx, "ping failed",
					slog.String("channel", channelName),
					slog.String("err", err.Error()))
				_ = sess.conn.Close(codingws.StatusGoingAway, "ping failed")
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
		if c.clientSessCount[sess.clientID] > 0 {
			c.clientSessCount[sess.clientID]--
			if c.clientSessCount[sess.clientID] == 0 {
				delete(c.clientSessCount, sess.clientID)
			}
		}
		c.metrics.Set(MetricSessionsBoundGauge, float64(len(c.sessions)), map[string]string{"channel": channelName})
	}
}

// Deliver implements channel.Channel.
//
// # Concurrency contract with Close
//
// N-1: Deliver and Close are concurrent-safe but not synchronized: a
// Deliver running when Close starts may race the per-session conn close
// and surface as a TransientFailure ("write: …" or "socket closed
// before ack"). The transient classification is correct — the
// scheduler retries on the next pod or the next bind — and the
// underlying conn library guards against double-close internally. The
// session map lookup runs under c.mu, so Deliver never sees a session
// after Close removed it; the only window is between "lookup returned
// sess" and "Write returned". Callers concerned about delivering a
// final notification on the way down should use the lifecycle module's
// drain phase rather than racing Close (S-7 #9 covers the goroutine
// join; this comment documents the Deliver-side window).
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
	var (
		ackCh chan struct{}
		w     *ackWaiter
	)
	if expectAck {
		w = sess.registerAck(env.Sequence)
		ackCh = w.ch
		// On exit, remove from map and ensure the channel is closed
		// exactly once. Without closeOnce on the cleanup path, a stray
		// late client ack arriving after deliverAck removed our entry
		// would close on a stale reference. With closeOnce, the close
		// is single-owner regardless of who fires first (B-18).
		defer func() {
			sess.cancelAck(env.Sequence)
			w.closeOnce()
		}()
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

// Close stops the channel and disconnects bound subscribers. S-7 #9:
// joins per-session goroutines via WaitGroup so Close blocks until both
// the ping loop and read loop have exited for every bound session,
// making shutdown deterministic.
func (c *Channel) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	sess := make([]*session, 0, len(c.sessions))
	for _, s := range c.sessions {
		sess = append(sess, s)
	}
	c.sessions = map[uuid.UUID]*session{}
	c.clientSessCount = map[string]int{}
	c.mu.Unlock()
	// Cancel the channel-wide ctx so ping loops bound to it return promptly.
	c.cancel()
	// Force-close every active socket; this unblocks the read loops.
	for _, s := range sess {
		_ = s.conn.Close(codingws.StatusGoingAway, "channel shutting down")
	}
	// Wait for every per-session goroutine to exit so the caller knows no
	// goroutine will write to a held resource after Close returns.
	c.wg.Wait()
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

// registerAck installs (or reuses) a single ackWaiter for eventNumber
// and returns it. If a waiter already exists (e.g., a duplicate Deliver
// call for the same sequence) the existing one is returned so both
// callers wait on the same single-owner close.
func (s *session) registerAck(eventNumber uint64) *ackWaiter {
	s.ackMu.Lock()
	defer s.ackMu.Unlock()
	if existing, ok := s.ackWaits[eventNumber]; ok {
		return existing
	}
	w := &ackWaiter{ch: make(chan struct{})}
	s.ackWaits[eventNumber] = w
	return w
}

// cancelAck removes the waiter for eventNumber from the map. It does
// NOT close the channel: the Deliver path that registered the waiter
// owns the lifecycle through closeOnce, which is safe to invoke from
// any goroutine. This keeps a stray late ack from observing a missing
// map entry plus a double-close.
func (s *session) cancelAck(eventNumber uint64) {
	s.ackMu.Lock()
	delete(s.ackWaits, eventNumber)
	s.ackMu.Unlock()
}

// deliverAck wakes any goroutine waiting on the given eventNumber. It
// is safe to invoke for unknown event numbers (no-op) and for the
// same eventNumber from multiple goroutines (sync.Once guards the
// close). Deliver removes the waiter from the map under ackMu before
// closing so a late client ack does not see a stale entry.
//
// B-18: previously this path called close(ch) directly while the
// matching Deliver could also close on the timeout path, panicking on
// close-of-closed-channel. The closeOnce wrapper makes the close
// single-owner regardless of who arrives first.
//
// Returns true when eventNumber matched a waiter, false otherwise.
// N-1: callers can use the false return to count "ack outside sent-set"
// frames as a signal of a misbehaving / hostile client.
func (s *session) deliverAck(eventNumber uint64) bool {
	s.ackMu.Lock()
	w, ok := s.ackWaits[eventNumber]
	if ok {
		delete(s.ackWaits, eventNumber)
	}
	s.ackMu.Unlock()
	if ok {
		w.closeOnce()
	}
	return ok
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

// splitProtocolList splits a Sec-WebSocket-Protocol header value into
// individual subprotocol tokens, trimming whitespace. RFC 6455 §11.3.4
// allows comma-separated lists per header line; the spec for clients
// is ambiguous so we accept both shapes (caller already iterates over
// Header.Values, this handles within-line commas).
func splitProtocolList(s string) []string {
	if s == "" {
		return nil
	}
	out := make([]string, 0, 1)
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] != ',' {
			continue
		}
		token := trimASCIIWhitespace(s[start:i])
		if token != "" {
			out = append(out, token)
		}
		start = i + 1
	}
	last := trimASCIIWhitespace(s[start:])
	if last != "" {
		out = append(out, last)
	}
	return out
}

func trimASCIIWhitespace(s string) string {
	for s != "" && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for s != "" && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
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
