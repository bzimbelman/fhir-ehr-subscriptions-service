// Copyright the fhir-subscriptions-foss authors.
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
	cfg = cfg.withDefaults()
	if metrics == nil {
		metrics = nopMetrics{}
	}
	if peerAddr == "" {
		if ra := conn.RemoteAddr(); ra != nil {
			peerAddr = ra.String()
		}
	}
	defer func() { _ = conn.Close() }()

	endpointLabels := map[string]string{"listener_endpoint": ep.Name}
	receiveLabels := map[string]string{"listener_endpoint": ep.Name, "peer_addr": peerAddr}

	framer := NewFramer(cfg.MaxMessageBytes)
	readBuf := make([]byte, 8192)

	var consecutivePersistFailures int

	// readCh is fed by a goroutine that does the blocking Read. We select
	// on it AND on ctx.Done() so ctx cancellation can trigger shutdown
	// without waiting for the read to return.
	type readResult struct {
		n   int
		err error
		buf []byte
	}
	readCh := make(chan readResult, 1)
	startRead := func() {
		// Set read deadline from the main goroutine to avoid racing with
		// the ctx.Done case below: we are the only writer of the deadline.
		if cfg.ReadIdleTimeout > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(cfg.ReadIdleTimeout))
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
					if framer.hasOpenFrame() {
						metrics.Inc(MetricDisconnectMidFrame, endpointLabels)
					}
					return
				}
				// Read deadline / timeout — close.
				if isTimeoutErr(r.err) {
					return
				}
				metrics.Inc(MetricReadErrorsTotal, endpointLabels)
				return
			}
			if r.n > 0 {
				framer.Append(r.buf)
				metrics.Add(MetricMessageBytes, float64(r.n), receiveLabels)
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
					return
				case FrameEvent:
					stop := handleOneFrame(ctx, conn, ev.Body, ep, cfg, persister, metrics,
						peerAddr, &consecutivePersistFailures, endpointLabels, receiveLabels)
					if stop {
						return
					}
				}
			}
			startRead()
		}
	}
}

// handleOneFrame runs the persist-then-ACK transaction for a single frame.
// Returns true to signal that the connection should be dropped (no further
// reads).
func handleOneFrame(
	ctx context.Context,
	conn net.Conn,
	body []byte,
	ep EndpointConfig,
	cfg ListenerConfig,
	persister Persister,
	metrics MetricsEmitter,
	peerAddr string,
	consecutivePersistFailures *int,
	endpointLabels, receiveLabels map[string]string,
) bool {
	now := time.Now().UTC()

	mshFields, mshErr := ExtractMSH(body)

	// allowed_message_types filter.
	if len(ep.AllowedMessageTypes) > 0 {
		if mshErr != nil {
			metrics.Inc(MetricNackTotal, map[string]string{
				"listener_endpoint": ep.Name, "reason": "msh9_unparseable",
			})
			metrics.Inc(MetricMessagesAckedTotal, map[string]string{
				"listener_endpoint": ep.Name, "outcome": OutcomeAE,
			})
			writeNACK(conn, mshFields, "msh9_unparseable", now)
			return false
		}
		if !messageTypeAllowed(mshFields.MessageType, ep.AllowedMessageTypes) {
			metrics.Inc(MetricNackTotal, map[string]string{
				"listener_endpoint": ep.Name, "reason": "message_type",
				"type": mshFields.MessageType,
			})
			metrics.Inc(MetricMessagesAckedTotal, map[string]string{
				"listener_endpoint": ep.Name, "outcome": OutcomeAE,
			})
			writeNACK(conn, mshFields, "message type not allowed", now)
			return false
		}
	}

	row := QueueRow{
		ID:               uuid.New(),
		ReceivedAt:       now,
		ListenerEndpoint: ep.Name,
		PeerAddr:         peerAddr,
		MLLPMessageID:    mshFields.MessageControlID,
		CorrelationID:    uuid.New(),
		Body:             append([]byte(nil), body...),
	}

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

	if err != nil {
		*consecutivePersistFailures++
		switch cfg.OnPersistFail {
		case OnPersistFailDrop:
			metrics.Inc(MetricDropForPersistFails, endpointLabels)
			metrics.Inc(MetricNackTotal, map[string]string{
				"listener_endpoint": ep.Name, "reason": persistFailureReason(err),
			})
			return true
		default: // OnPersistFailNack
			metrics.Inc(MetricNackTotal, map[string]string{
				"listener_endpoint": ep.Name, "reason": persistFailureReason(err),
			})
			metrics.Inc(MetricMessagesAckedTotal, map[string]string{
				"listener_endpoint": ep.Name, "outcome": OutcomeAE,
			})
			writeNACK(conn, mshFields, persistErrorReason(err), now)
			if *consecutivePersistFailures >= cfg.NackThenDropAfter {
				metrics.Inc(MetricDropForPersistFails, endpointLabels)
				return true
			}
			return false
		}
	}

	*consecutivePersistFailures = 0
	metrics.Inc(MetricMessagesReceivedTotal, receiveLabels)
	metrics.Inc(MetricMessagesAckedTotal, map[string]string{
		"listener_endpoint": ep.Name, "outcome": OutcomeAA,
	})
	writeACK(conn, mshFields, now)
	return false
}

func writeACK(conn net.Conn, msh MSHFields, now time.Time) {
	frame := buildACK(ackAA, msh, "", now)
	_ = writeFrame(conn, frame)
}

func writeNACK(conn net.Conn, msh MSHFields, reason string, now time.Time) {
	frame := buildACK(ackAE, msh, reason, now)
	_ = writeFrame(conn, frame)
}

func writeFrame(conn net.Conn, frame []byte) error {
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
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
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	// net.Pipe surfaces "io: read/write on closed pipe".
	return strings.Contains(err.Error(), "closed")
}

// hasOpenFrame is exposed on Framer for the connection task's
// disconnect-mid-frame metric. Defined here as a small accessor.
func (f *Framer) hasOpenFrame() bool {
	return f.state == stateOpen || len(f.buf) > 0
}
