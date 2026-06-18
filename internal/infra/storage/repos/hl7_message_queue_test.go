// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package repos_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v3"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/codec"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

func newCodec(t *testing.T) *codec.Codec {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	kp := codec.NewStaticKeyProvider(map[int32][]byte{1: key}, 1)
	c, err := codec.New(kp)
	if err != nil {
		t.Fatalf("codec: %v", err)
	}
	return c
}

func TestHl7MessageQueueInsertReturnsID(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	wantID := uuid.New()
	corr := uuid.New()

	pool.ExpectQuery("INSERT INTO hl7_message_queue").
		WithArgs(
			pgxmock.AnyArg(), // listener_endpoint
			pgxmock.AnyArg(), // peer_addr
			pgxmock.AnyArg(), // mllp_message_id
			pgxmock.AnyArg(), // correlation_id
			pgxmock.AnyArg(), // raw_body (encrypted)
			pgxmock.AnyArg(), // key_version
		).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(wantID))

	repo := repos.NewHl7MessageQueueRepo(newCodec(t))
	got, err := repo.Insert(ctx, pool, repos.Hl7MessageQueueRow{
		ListenerEndpoint: "adt-feed",
		PeerAddr:         "10.0.0.1:5000",
		MllpMessageID:    "ctrl-1",
		CorrelationID:    corr,
		RawBody:          []byte("MSH|...HL7..."),
		ReceivedAt:       time.Now(),
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if got != wantID {
		t.Fatalf("expected %v got %v", wantID, got)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestHl7MessageQueueClaimUnprocessed(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	c := newCodec(t)
	bodyA := []byte("MSH|hl7|a")
	bodyB := []byte("MSH|hl7|b")
	encA, _, err := c.Encrypt(bodyA)
	if err != nil {
		t.Fatal(err)
	}
	encB, _, err := c.Encrypt(bodyB)
	if err != nil {
		t.Fatal(err)
	}
	idA := uuid.New()
	idB := uuid.New()
	corrA := uuid.New()
	corrB := uuid.New()
	now := time.Now()

	pool.ExpectBegin()
	pool.ExpectQuery("FOR UPDATE SKIP LOCKED").
		WithArgs(int32(10)).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "listener_endpoint", "peer_addr", "received_at",
			"mllp_message_id", "correlation_id", "raw_body", "key_version",
		}).
			AddRow(idA, "adt", "10.0.0.1:5000", now, "m1", corrA, encA, int32(1)).
			AddRow(idB, "adt", "10.0.0.2:5000", now, "m2", corrB, encB, int32(1)))
	pool.ExpectCommit()

	repo := repos.NewHl7MessageQueueRepo(c)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := repo.ClaimUnprocessed(ctx, tx, 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows got %d", len(rows))
	}
	if string(rows[0].RawBody) != string(bodyA) {
		t.Errorf("row 0 body decrypted incorrectly: got %q", rows[0].RawBody)
	}
	if string(rows[1].RawBody) != string(bodyB) {
		t.Errorf("row 1 body decrypted incorrectly: got %q", rows[1].RawBody)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestHl7MessageQueueMarkProcessed(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	id := uuid.New()
	pool.ExpectExec("UPDATE hl7_message_queue").
		WithArgs(id).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	repo := repos.NewHl7MessageQueueRepo(newCodec(t))
	n, err := repo.MarkProcessed(ctx, pool, id)
	if err != nil {
		t.Fatalf("mark: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 row updated, got %d", n)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}
