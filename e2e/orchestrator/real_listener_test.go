// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/mllp"
)

// realMLLPListener wraps the production MLLP listener (internal/mllp) so
// the e2e harness drives the real persistence-then-ACK code path, not a
// stub that happened to look like it.
//
// Compared to the stub it replaces:
//   - Same persist-then-ACK contract (the listener's persister does the
//     same INSERT INTO hl7_message_queue and only ACKs after COMMIT).
//   - Real framer (handles the spec's framing edge cases via
//     internal/mllp.framer rather than the stub's hand-rolled splitter).
//   - Real MSH-10 extractor and ACK/NACK builders.
//
// What the harness no longer owns: the framing primitives and the ACK
// shape. Both move into the SUT-side code, where they belong.
type realMLLPListener struct {
	listener *mllp.Listener
	endpoint string
	addr     net.Addr
}

// startRealMLLPListener stands up the real internal/mllp listener bound
// on 127.0.0.1:0 and a Postgres-backed Persister that writes to
// hl7_message_queue using the supplied pool. Returns a handle whose
// Addr() and Close() match the stub's surface so setup_test.go can swap
// in / out without other changes.
func startRealMLLPListener(ctx context.Context, pool *pgxpool.Pool) (*realMLLPListener, error) {
	endpointName := "stub-feed" // keep the same listener_endpoint label so existing assertions still match
	cfg := mllp.ListenerConfig{
		Endpoints: []mllp.EndpointConfig{{
			Name: endpointName,
			Bind: "127.0.0.1:0",
		}},
		MaxMessageBytes:    1 << 20,
		ReadIdleTimeout:    30 * time.Second,
		PersistTimeout:     5 * time.Second,
		NackThenDropAfter:  5,
		ShutdownDrainGrace: 5 * time.Second,
		InflightCapPerConn: 64,
		OnPersistFail:      mllp.OnPersistFailNack,
	}

	persister := &pgxPersister{pool: pool}

	l := mllp.New(cfg, persister, nil /* metrics */, nil /* logger */)
	if err := l.Start(ctx); err != nil {
		return nil, fmt.Errorf("real mllp listener start: %w", err)
	}
	addr := l.Addr(endpointName)
	if addr == nil {
		_ = l.Shutdown(ctx)
		return nil, fmt.Errorf("real mllp listener: endpoint %q has no bound address", endpointName)
	}
	return &realMLLPListener{
		listener: l,
		endpoint: endpointName,
		addr:     addr,
	}, nil
}

// Addr returns the bound address.
func (r *realMLLPListener) Addr() net.Addr { return r.addr }

// Close drives the listener through its graceful-shutdown path.
func (r *realMLLPListener) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.listener.Shutdown(ctx)
}

// pgxPersister implements mllp.Persister against a pgxpool. It does the
// minimal INSERT the test harness needs; the production path will go
// through internal/infra/storage's repository in a later commit.
type pgxPersister struct {
	pool *pgxpool.Pool
}

func (p *pgxPersister) Persist(ctx context.Context, row mllp.QueueRow) error {
	id := row.ID
	if id == uuid.Nil {
		id = uuid.New()
	}
	_, err := p.pool.Exec(ctx, `
		insert into hl7_message_queue
		  (id, listener_endpoint, peer_addr, mllp_message_id,
		   correlation_id, raw_body, received_at)
		values
		  ($1, $2, $3, NULLIF($4, ''), $5::uuid, $6, $7)
	`, id, row.ListenerEndpoint, row.PeerAddr, row.MLLPMessageID,
		row.CorrelationID, row.Body, row.ReceivedAt)
	if err != nil {
		return fmt.Errorf("%w: %v", mllp.ErrPersistTransient, err)
	}
	return nil
}
