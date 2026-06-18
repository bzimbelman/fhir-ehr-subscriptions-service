// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StubListener is the v1 stand-in for the production MLLP listener. It
// implements just enough of the listener LLD's persistence-then-ACK
// contract to drive the smoke_listener_ack and smoke_persist scenarios:
//
//   * Accept TCP on 127.0.0.1:0.
//   * Read MLLP-framed messages (start 0x0B, end 0x1C 0x0D).
//   * Insert each into hl7_message_queue inside one transaction.
//   * After COMMIT, write back an MSA|AA|<MSH-10> ACK.
//   * On persist failure, NACK with MSA|AE|<MSH-10>.
//
// The stub does NOT implement: TLS, allowed_message_types filtering, the
// inflight cap, the consecutive-failure-then-drop behavior, or the
// startup readiness gate. Those land with the real component.
type StubListener struct {
	listener net.Listener
	pool     *pgxpool.Pool
	endpoint string

	mu      sync.Mutex
	wg      sync.WaitGroup
	closed  bool
	ctx     context.Context
	cancel  context.CancelFunc
}

func startStubMLLPListener(ctx context.Context, pool *pgxpool.Pool) (*StubListener, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	listenerCtx, cancel := context.WithCancel(context.Background())
	s := &StubListener{
		listener: l,
		pool:     pool,
		endpoint: "stub-feed",
		ctx:      listenerCtx,
		cancel:   cancel,
	}
	s.wg.Add(1)
	go s.acceptLoop()
	_ = ctx
	return s, nil
}

// Addr returns the bound address.
func (s *StubListener) Addr() net.Addr { return s.listener.Addr() }

// Close stops accepting and waits for in-flight connections to drain.
func (s *StubListener) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	_ = s.listener.Close()
	s.wg.Wait()
	return nil
}

func (s *StubListener) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
			continue
		}
		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			defer c.Close()
			s.serveConnection(c)
		}(conn)
	}
}

func (s *StubListener) serveConnection(conn net.Conn) {
	buf := make([]byte, 4096)
	var acc bytes.Buffer
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := conn.Read(buf)
		if n > 0 {
			acc.Write(buf[:n])
			for {
				if !s.consumeOneFrame(conn, &acc) {
					break
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// consumeOneFrame extracts one full MLLP frame from acc and writes back
// an ACK (or NACK on persist failure). Returns false if no full frame is
// available yet.
func (s *StubListener) consumeOneFrame(conn net.Conn, acc *bytes.Buffer) bool {
	start := bytes.IndexByte(acc.Bytes(), 0x0B)
	end := bytes.Index(acc.Bytes(), []byte{0x1C, 0x0D})
	if start < 0 || end < 0 || end <= start {
		return false
	}
	body := append([]byte(nil), acc.Bytes()[start+1:end]...)
	rest := append([]byte(nil), acc.Bytes()[end+2:]...)
	acc.Reset()
	acc.Write(rest)

	msh10 := extractMSH10(body)
	persistErr := s.persistMessage(body, msh10, conn.RemoteAddr().String())
	ack := buildACK(persistErr == nil, msh10)
	_, _ = conn.Write(frameMLLP(ack))
	return true
}

func (s *StubListener) persistMessage(body []byte, mshID, peerAddr string) error {
	corrID := uuid.NewString()
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		insert into hl7_message_queue
		  (listener_endpoint, peer_addr, mllp_message_id,
		   correlation_id, raw_body)
		values
		  ($1, $2, $3, $4::uuid, $5)
	`, s.endpoint, peerAddr, mshID, corrID, body); err != nil {
		return fmt.Errorf("insert hl7_message_queue: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// --- Local copies of the MLLP framing primitives. We deliberately do not
// import the mockehr package here because that would create a cyclic
// dependency in spirit (the listener stub is the receiver-side analog
// of the EHR's sender-side framing). Keeping these in this file keeps
// the orchestrator self-contained.

func frameMLLP(body []byte) []byte {
	out := make([]byte, 0, len(body)+3)
	out = append(out, 0x0B)
	out = append(out, body...)
	out = append(out, 0x1C, 0x0D)
	return out
}

func extractMSH10(body []byte) string {
	end := bytes.IndexByte(body, '\r')
	if end < 0 {
		end = len(body)
	}
	parts := strings.Split(string(body[:end]), "|")
	if len(parts) <= 9 {
		return ""
	}
	return parts[9]
}

func buildACK(accept bool, ctrlID string) []byte {
	code := "AA"
	if !accept {
		code = "AE"
	}
	now := time.Now().UTC().Format("20060102150405")
	msh := fmt.Sprintf("MSH|^~\\&|FHIRSUBS|TEST|MOCKEHR|E2E|%s||ACK|ACK-%s|T|2.5.1\r", now, ctrlID)
	msa := fmt.Sprintf("MSA|%s|%s\r", code, ctrlID)
	return []byte(msh + msa)
}
