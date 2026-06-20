// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
)

// connectionLogger wraps the package-level Logger with the per-connection
// fields (endpoint, peer_addr, connection_id) so call sites only supply
// the event name and any per-event extras. nil-safe.
type connectionLogger struct {
	log    Logger
	fields map[string]any
}

func newConnectionLogger(log Logger, ep, peer string, connID uuid.UUID) connectionLogger {
	if log == nil {
		log = nopLogger{}
	}
	return connectionLogger{
		log: log,
		fields: map[string]any{
			"listener_endpoint": ep,
			"peer_addr":         peer,
			"connection_id":     connID.String(),
		},
	}
}

func (l connectionLogger) emit(level int, event string, extra map[string]any) {
	merged := make(map[string]any, len(l.fields)+len(extra)+1)
	for k, v := range l.fields {
		merged[k] = v
	}
	for k, v := range extra {
		merged[k] = v
	}
	merged["event"] = event
	switch level {
	case logLevelInfo:
		l.log.Info(event, merged)
	case logLevelWarn:
		l.log.Warn(event, merged)
	case logLevelError:
		l.log.Error(event, merged)
	}
}

const (
	logLevelInfo = iota
	logLevelWarn
	logLevelError
)

// HandleConnection runs the per-connection read/persist/ACK loop on conn.
// It returns when the peer closes the connection, the framer reports a
// malformed stream, the persist failure threshold is reached, the read
// idle timeout fires, or ctx is canceled. It always closes conn before
// returning.
//
// HandleConnection is exposed at package level (rather than as a method on
// Listener) because the unit tests drive it directly with net.Pipe — there
// is no TCP layer to set up. The Listener wraps it for production.
func HandleConnection(
	ctx context.Context,
	conn net.Conn,
	ep EndpointConfig,
	cfg ListenerConfig,
	persister Persister,
	metrics MetricsEmitter,
	peerAddr string,
) {
	HandleConnectionWithLogger(ctx, conn, ep, cfg, persister, metrics, nopLogger{}, peerAddr)
}

// HandleConnectionWithLogger is HandleConnection with an explicit Logger.
// The Listener uses this in production; tests use HandleConnection.
func HandleConnectionWithLogger(
	ctx context.Context,
	conn net.Conn,
	ep EndpointConfig,
	cfg ListenerConfig,
	persister Persister,
	metrics MetricsEmitter,
	logger Logger,
	peerAddr string,
) {
	cfg = cfg.withDefaults()
	if metrics == nil {
		metrics = nopMetrics{}
	}
	if logger == nil {
		logger = nopLogger{}
	}
	if peerAddr == "" {
		if ra := conn.RemoteAddr(); ra != nil {
			peerAddr = ra.String()
		}
	}
	state := newConnectionState(ep.Name, peerAddr)
	clog := newConnectionLogger(logger, ep.Name, peerAddr, state.id)
	clog.emit(logLevelInfo, "accept", nil)
	defer func() {
		_ = conn.Close()
		clog.emit(logLevelInfo, "disconnect", nil)
	}()

	endpointLabels := map[string]string{"listener_endpoint": ep.Name}

	framer := NewFramer(cfg.MaxMessageBytes)
	readBuf := make([]byte, cfg.ReadBufBytes)

	// assemblyStart tracks when the framer transitioned from "no frame in
	// flight" to "frame in flight" — i.e. the first byte after the most
	// recent 0x0B. Zero means no frame is in flight. The read loop uses
	// this to arm the FrameAssemblyTimeout deadline (S-9.1): the deadline
	// runs only while we are actively assembling a frame, not while the
	// connection is idle between frames.
	var assemblyStart time.Time

	// frameDeadlineFired records whether the most recent read deadline
	// expiry was the frame-assembly deadline (vs the read-idle deadline).
	// We need this to disambiguate after net.OpError surfaces a generic
	// timeout — Read() doesn't tell us which deadline fired.
	frameDeadlineFired := false

	// readCh is fed by a goroutine that does the blocking Read. We select
	// on it AND on ctx.Done() so ctx cancellation can trigger shutdown
	// without waiting for the read to return.
	//
	// OP #228 — invariant under test: readCh MUST be buffered with
	// capacity >= 1. The startRead goroutine writes the result of its
	// blocking Read into readCh and then exits. The main loop selects on
	// readCh and ctx.Done(). When ctx fires the main loop closes the
	// connection, drains readCh once (`<-readCh`) and returns. The
	// buffer guarantees the read goroutine's send succeeds even if the
	// main loop has already entered the `<-ctx.Done()` arm — without
	// the buffer the send would block forever after the main loop
	// stopped selecting on readCh, leaking the goroutine. Cap-1 is the
	// minimum that satisfies the invariant; a `select { case readCh
	// <- out: case <-ctx.Done(): }` would also work but adds a select
	// per Read on the hot path. The buffered-channel pattern is the
	// long-standing choice; goleak coverage in connection_goleak_test.go
	// guards against regression.
	type readResult struct {
		n   int
		err error
		buf []byte
	}
	readCh := make(chan readResult, 1)
	startRead := func() {
		// Compute the effective read deadline as the earlier of the
		// read-idle timeout (silent connection) and the frame-assembly
		// timeout (peer streamed start byte but never finishes). Setting
		// the deadline from the main goroutine avoids racing with the
		// ctx.Done case below: we are the only writer of the deadline.
		var deadline time.Time
		if cfg.ReadIdleTimeout > 0 {
			deadline = time.Now().Add(cfg.ReadIdleTimeout)
		}
		frameDeadlineFired = false
		if cfg.FrameAssemblyTimeout > 0 && !assemblyStart.IsZero() {
			frameDeadline := assemblyStart.Add(cfg.FrameAssemblyTimeout)
			if deadline.IsZero() || frameDeadline.Before(deadline) {
				deadline = frameDeadline
				frameDeadlineFired = true
			}
		}
		if !deadline.IsZero() {
			_ = conn.SetReadDeadline(deadline)
		}
		go func() {
			n, err := conn.Read(readBuf)
			out := readResult{n: n, err: err}
			if n > 0 {
				out.buf = append([]byte(nil), readBuf[:n]...)
			}
			readCh <- out
		}()
	}

	startRead()

	for {
		select {
		case <-ctx.Done():
			// Best-effort: close conn to unblock the in-flight Read so its
			// goroutine exits. Closing the conn is final; the deferred
			// Close at the top is a no-op after this.
			_ = conn.Close()
			<-readCh
			return
		case r := <-readCh:
			if r.err != nil {
				if errors.Is(r.err, io.EOF) || isClosedConnErr(r.err) {
					if final, ok := framer.Finalize().(MalformedEvent); ok {
						metrics.Inc(MetricDisconnectMidFrame, endpointLabels)
						metrics.Inc(MetricMalformedTotal, map[string]string{
							"listener_endpoint": ep.Name,
							"reason":            string(final.Reason),
						})
						clog.emit(logLevelWarn, "disconnect_mid_frame", map[string]any{
							"reason": string(final.Reason),
						})
					}
					return
				}
				// Read deadline / timeout — close. The deadline may be
				// either the read-idle timeout (silent connection) or
				// the frame-assembly timeout (S-9.1, peer streamed
				// start byte but never finished); frameDeadlineFired
				// tells us which.
				if isTimeoutErr(r.err) {
					if frameDeadlineFired {
						metrics.Inc(MetricFrameDeadlineExceeded, endpointLabels)
						clog.emit(logLevelWarn, "frame_deadline_exceeded", map[string]any{
							"frame_assembly_timeout": cfg.FrameAssemblyTimeout.String(),
							"error":                  ErrFrameDeadline.Error(),
						})
					}
					return
				}
				metrics.Inc(MetricReadErrorsTotal, endpointLabels)
				clog.emit(logLevelWarn, "read_error", map[string]any{"error": r.err.Error()})
				return
			}
			if r.n > 0 {
				if appendErr := framer.Append(r.buf); appendErr != nil {
					// OP #227: Framer.Append rejected the chunk because
					// it would push pending past 2*maxBody. Treat
					// identically to a malformed event with reason
					// oversized_message — the connection loop drops the
					// peer.
					metrics.Inc(MetricMalformedTotal, map[string]string{
						"listener_endpoint": ep.Name,
						"reason":            string(ReasonOversizedMessage),
					})
					clog.emit(logLevelWarn, "malformed", map[string]any{
						"reason": string(ReasonOversizedMessage),
						"error":  appendErr.Error(),
					})
					return
				}
				metrics.Add(MetricMessageBytes, float64(r.n), endpointLabels)
			}

		drainFrames:
			for {
				switch ev := framer.Next().(type) {
				case NeedMoreEvent:
					break drainFrames
				case MalformedEvent:
					metrics.Inc(MetricMalformedTotal, map[string]string{
						"listener_endpoint": ep.Name,
						"reason":            string(ev.Reason),
					})
					clog.emit(logLevelWarn, "malformed", map[string]any{"reason": string(ev.Reason)})
					return
				case FrameEvent:
					draining := ctx.Err() != nil
					stop := handleOneFrame(ctx, conn, ev.Body, ep, cfg, persister, metrics, clog,
						state, endpointLabels, draining)
					if stop {
						return
					}
				}
			}
			// S-9.1: arm / disarm the per-frame assembly deadline based on
			// whether the framer is currently mid-frame. The deadline runs
			// only while a frame is in flight; idle connections are
			// governed by ReadIdleTimeout.
			if framer.AssemblyInProgress() {
				if assemblyStart.IsZero() {
					assemblyStart = time.Now()
				}
			} else {
				assemblyStart = time.Time{}
			}
			startRead()
		}
	}
}

// handleOneFrame runs the persist-then-ACK transaction for a single frame.
// Returns true to signal that the connection should be dropped (no further
// reads).
//
// The state pointer carries the per-connection inflight counter (LLD §5.6
// gate) and consecutive-failure counter (LLD §5.6 NACK-then-drop ramp).
// `draining` is true when ctx has been canceled (graceful shutdown); per
// LLD §5.7 we do NOT NACK on persist failure during drain.
func handleOneFrame(
	ctx context.Context,
	conn net.Conn,
	body []byte,
	ep EndpointConfig,
	cfg ListenerConfig,
	persister Persister,
	metrics MetricsEmitter,
	clog connectionLogger,
	state *connectionState,
	endpointLabels map[string]string,
	draining bool,
) bool {
	now := time.Now().UTC()

	mshFields, mshErr := ExtractMSH(body)

	// Inflight cap gate (LLD §5.6). Cap == 0 disables the cap; otherwise
	// the listener NACKs without calling persist.
	if cfg.InflightCapPerConn > 0 && int(state.inflightCount()) >= cfg.InflightCapPerConn {
		metrics.Inc(MetricNackTotal, map[string]string{
			"listener_endpoint": ep.Name, "reason": "inflight_cap",
		})
		metrics.Inc(MetricMessagesAckedTotal, map[string]string{
			"listener_endpoint": ep.Name, "outcome": OutcomeAE,
		})
		clog.emit(logLevelWarn, "nacked", map[string]any{
			"reason":          "inflight_cap",
			"mllp_message_id": mshFields.MessageControlID,
		})
		_ = writeNACK(conn, mshFields, "inflight cap reached", now, cfg.AckWriteTimeout)
		return false
	}

	// allowed_message_types filter.
	if len(ep.AllowedMessageTypes) > 0 {
		if mshErr != nil {
			metrics.Inc(MetricNackTotal, map[string]string{
				"listener_endpoint": ep.Name, "reason": "msh9_unparseable",
			})
			metrics.Inc(MetricMessagesAckedTotal, map[string]string{
				"listener_endpoint": ep.Name, "outcome": OutcomeAE,
			})
			clog.emit(logLevelWarn, "nacked", map[string]any{
				"reason":          "msh9_unparseable",
				"mllp_message_id": mshFields.MessageControlID,
			})
			_ = writeNACK(conn, mshFields, "msh9_unparseable", now, cfg.AckWriteTimeout)
			return false
		}
		if !messageTypeAllowed(mshFields.MessageType, ep.AllowedMessageTypes) {
			metrics.Inc(MetricNackTotal, map[string]string{
				"listener_endpoint": ep.Name, "reason": "message_type",
				"type": bucketMessageTypeLabel(mshFields.MessageType, ep.AllowedMessageTypes),
			})
			metrics.Inc(MetricMessagesAckedTotal, map[string]string{
				"listener_endpoint": ep.Name, "outcome": OutcomeAE,
			})
			clog.emit(logLevelWarn, "nacked", map[string]any{
				"reason":          "message_type",
				"type":            mshFields.MessageType,
				"mllp_message_id": mshFields.MessageControlID,
			})
			_ = writeNACK(conn, mshFields, "message type not allowed", now, cfg.AckWriteTimeout)
			return false
		}
	}

	correlationID := uuid.New()
	row := QueueRow{
		ID:               uuid.New(),
		ReceivedAt:       now,
		ListenerEndpoint: ep.Name,
		PeerAddr:         state.peerAddr,
		MLLPMessageID:    mshFields.MessageControlID,
		CorrelationID:    correlationID,
		// N-1: framer already hands us a fresh slice (see Framer.Next
		// where body := append([]byte(nil), f.buf...)); the redundant
		// copy here was a no-op double-allocation.
		Body: body,
	}
	clog.emit(logLevelInfo, "frame_received", map[string]any{
		"received_at":     now.Format(time.RFC3339Nano),
		"correlation_id":  correlationID.String(),
		"mllp_message_id": mshFields.MessageControlID,
		"bytes":           len(body),
	})

	// Track inflight across the persist call: LLD §7 inflight gauge and
	// the LLD §5.6 cap gate above both rely on this counter. The gauge
	// is endpoint-labeled only per LLD §7 (per-connection cardinality
	// stays on log lines, not metric labels).
	state.incInflight()
	metrics.Set(MetricInflightPerConnection, float64(state.inflightCount()), endpointLabels)
	defer func() {
		state.decInflight()
		metrics.Set(MetricInflightPerConnection, float64(state.inflightCount()), endpointLabels)
	}()

	persistCtx, cancel := context.WithTimeout(context.Background(), cfg.PersistTimeout)
	// We deliberately do not chain off ctx for persistCtx: the LLD's
	// shutdown drain rule says in-flight persists complete. ctx
	// cancellation triggers shutdown of the read loop only; this in-flight
	// persist runs to completion against its own timeout.
	_ = ctx
	defer cancel()

	persistStart := time.Now()
	err := persister.Persist(persistCtx, row)
	metrics.Observe(MetricPersistDurationMS,
		float64(time.Since(persistStart).Microseconds())/1000.0,
		endpointLabels)

	consecutiveFails := state.recordPersistResult(err)

	if err != nil {
		reason := persistFailureReason(err)

		// LLD §5.7: during drain, do not NACK on persist failure. Drop
		// silently and exit. The EHR will reconnect.
		if draining {
			clog.emit(logLevelWarn, "dropped", map[string]any{
				"reason":          "drain_persist_failure",
				"correlation_id":  correlationID.String(),
				"mllp_message_id": mshFields.MessageControlID,
				"error":           err.Error(),
			})
			return true
		}

		switch cfg.OnPersistFail {
		case OnPersistFailDrop:
			metrics.Inc(MetricDropForPersistFails, endpointLabels)
			metrics.Inc(MetricNackTotal, map[string]string{
				"listener_endpoint": ep.Name, "reason": reason,
			})
			clog.emit(logLevelError, "dropped", map[string]any{
				"reason":          reason,
				"correlation_id":  correlationID.String(),
				"mllp_message_id": mshFields.MessageControlID,
				"error":           err.Error(),
			})
			return true
		default: // OnPersistFailNack
			metrics.Inc(MetricNackTotal, map[string]string{
				"listener_endpoint": ep.Name, "reason": reason,
			})
			metrics.Inc(MetricMessagesAckedTotal, map[string]string{
				"listener_endpoint": ep.Name, "outcome": OutcomeAE,
			})
			level := logLevelWarn
			if errors.Is(err, ErrPersistPermanent) {
				level = logLevelError
			}
			clog.emit(level, "nacked", map[string]any{
				"reason":          reason,
				"correlation_id":  correlationID.String(),
				"mllp_message_id": mshFields.MessageControlID,
				"error":           err.Error(),
			})
			_ = writeNACK(conn, mshFields, persistErrorReason(err), now, cfg.AckWriteTimeout)
			if int(consecutiveFails) >= cfg.NackThenDropAfter {
				metrics.Inc(MetricDropForPersistFails, endpointLabels)
				clog.emit(logLevelError, "dropped", map[string]any{
					"reason":   "consecutive_persist_failures",
					"failures": consecutiveFails,
				})
				return true
			}
			return false
		}
	}

	metrics.Inc(MetricMessagesReceivedTotal, endpointLabels)
	metrics.Inc(MetricMessagesAckedTotal, map[string]string{
		"listener_endpoint": ep.Name, "outcome": OutcomeAA,
	})
	clog.emit(logLevelInfo, "persisted", map[string]any{
		"correlation_id":  correlationID.String(),
		"mllp_message_id": mshFields.MessageControlID,
		"row_id":          row.ID.String(),
	})
	if err := writeACK(conn, mshFields, now, cfg.AckWriteTimeout); err != nil {
		// Per LLD §8: ACK write failure after commit does not change row
		// state — the row is durable, the EHR will time out and retry,
		// and downstream idempotency dedupes. Log warn so operators can
		// see the EHR-side blip.
		clog.emit(logLevelWarn, "ack_write_failed", map[string]any{
			"outcome":         OutcomeAA,
			"correlation_id":  correlationID.String(),
			"mllp_message_id": mshFields.MessageControlID,
			"error":           err.Error(),
		})
		return false
	}
	clog.emit(logLevelInfo, "acked", map[string]any{
		"outcome":         OutcomeAA,
		"correlation_id":  correlationID.String(),
		"mllp_message_id": mshFields.MessageControlID,
	})
	return false
}

// defaultAckWriteTimeout backstops writeACK / writeNACK when the caller
// did not pass through a configured AckWriteTimeout (typically older
// tests). Default mirrors the pre-N-1 hardcoded 2s.
const defaultAckWriteTimeout = 2 * time.Second

func writeACK(conn net.Conn, msh MSHFields, now time.Time, timeout time.Duration) error {
	frame := buildACK(ackAA, msh, "", now)
	return writeFrame(conn, frame, timeout)
}

func writeNACK(conn net.Conn, msh MSHFields, reason string, now time.Time, timeout time.Duration) error {
	frame := buildACK(ackAE, msh, reason, now)
	return writeFrame(conn, frame, timeout)
}

func writeFrame(conn net.Conn, frame []byte, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = defaultAckWriteTimeout
	}
	_ = conn.SetWriteDeadline(time.Now().Add(timeout))
	_, err := conn.Write(frame)
	_ = conn.SetWriteDeadline(time.Time{})
	return err
}

func messageTypeAllowed(t string, allowed []string) bool {
	for _, a := range allowed {
		if a == t {
			return true
		}
	}
	return false
}

// bucketMessageTypeLabel returns t verbatim when it is in the allowed
// set, and the literal "other" otherwise. The Prometheus nack_total
// metric uses this for its "type" label so a hostile peer flooding
// distinct MSH-9 values cannot create a new time-series per value
// (B-30).
func bucketMessageTypeLabel(t string, allowed []string) string {
	for _, a := range allowed {
		if a == t {
			return t
		}
	}
	return "other"
}

// persistFailureReason maps a persister error to a metric-label value
// so operators can split transient vs. permanent failures.
func persistFailureReason(err error) string {
	switch {
	case errors.Is(err, ErrPersistPermanent):
		return "persist_permanent"
	case errors.Is(err, ErrPersistTransient):
		return "persist_transient"
	case isContextErr(err):
		return "persist_timeout"
	default:
		return "persist_transient"
	}
}

// persistErrorReason renders the failure as text for the NACK MSA-3 field.
func persistErrorReason(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func isTimeoutErr(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func isClosedConnErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	// net.Pipe surfaces specific sentinel strings; we match those
	// narrowly rather than any error containing the word "closed"
	// (S-9.3) — vendor errors like "JWKS host closed for maintenance"
	// would otherwise be misclassified.
	msg := err.Error()
	for _, marker := range []string{
		"io: read/write on closed pipe",
		"use of closed network connection",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
