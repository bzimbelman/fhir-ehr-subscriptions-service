//go:build integration

// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Integration tests for the websocket channel. Run with:
//
//	go test -race -tags integration ./internal/channel/websocket/...
//
// Requires Docker; skips cleanly when a Postgres container cannot be
// started so CI environments without Docker do not flake.
package websocket_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	codingws "github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/websocket"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/migrate"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

func startPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// testcontainers panics rather than returning an error when no Docker
	// host can be discovered. Recover so this test family Skips cleanly
	// instead of crashing the suite on Docker-less developer machines.
	var (
		container interface {
			Terminate(context.Context) error
			ConnectionString(context.Context, ...string) (string, error)
		}
		runErr error
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				runErr = fmt.Errorf("docker provider panic: %v", r)
			}
		}()
		c, err := tcpostgres.Run(ctx,
			"postgres:16-alpine",
			tcpostgres.WithDatabase("ws_test"),
			tcpostgres.WithUsername("test"),
			tcpostgres.WithPassword("test"),
			tcpostgres.BasicWaitStrategies(),
			tcpostgres.WithSQLDriver("pgx/v5"),
		)
		runErr = err
		if c != nil {
			container = c
		}
	}()
	if runErr != nil || container == nil {
		t.Skipf("postgres container unavailable; skipping: %v", runErr)
	}
	t.Cleanup(func() {
		_ = container.Terminate(context.Background())
	})

	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Skipf("connection string unavailable: %v", err)
	}

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	return pool
}

// repoTokenConsumer adapts the real WsBindingTokensRepo into the channel's
// TokenConsumer interface.
type repoTokenConsumer struct {
	repo *repos.WsBindingTokensRepo
	pool *pgxpool.Pool
}

func (a *repoTokenConsumer) Consume(ctx context.Context, token string, now time.Time) (websocket.ConsumeResult, error) {
	r, err := a.repo.Consume(ctx, a.pool, token, now)
	if err != nil {
		return websocket.ConsumeResult{}, err
	}
	return websocket.ConsumeResult{
		Outcome:        websocket.ConsumeOutcome(r.Outcome),
		SubscriptionID: r.SubscriptionID,
		ClientID:       r.ClientID,
	}, nil
}

// noopReplayer satisfies EventReplayer with empty replays.
type noopReplayer struct{}

func (noopReplayer) ReplaySince(context.Context, uuid.UUID, uint64) ([]websocket.PastEvent, error) {
	return nil, nil
}

// seedSubscription inserts an auth_clients row plus a subscription so the
// foreign keys on ws_binding_tokens are satisfied.
func seedSubscription(t *testing.T, pool *pgxpool.Pool) (subID uuid.UUID, clientID string) {
	t.Helper()
	clientID = "ws-int-test"
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`insert into auth_clients (id, jwks_url, scopes, display_name)
		 values ($1, $2, $3, $4)
		 on conflict do nothing`,
		clientID, "https://example.test/jwks", []string{"system/Subscription.crud"}, "ws-int-test",
	); err != nil {
		t.Fatalf("seed auth_clients: %v", err)
	}
	subID = uuid.New()
	if _, err := pool.Exec(ctx,
		`insert into subscriptions
		 (id, client_id, status, topic_url, channel_type, endpoint)
		 values ($1, $2, 'active', 'http://example.org/topic', 'websocket', null)`,
		subID, clientID,
	); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	return subID, clientID
}

func TestIntegrationBindWithRealRepo(t *testing.T) {
	t.Parallel()
	pool := startPostgres(t)

	subID, clientID := seedSubscription(t, pool)
	repo := repos.NewWsBindingTokensRepo()
	if err := repo.Insert(context.Background(), pool, repos.WsBindingTokenRow{
		Token:          "real-token",
		SubscriptionID: subID,
		ClientID:       clientID,
		ExpiresAt:      time.Now().Add(60 * time.Second),
	}); err != nil {
		t.Fatalf("insert token: %v", err)
	}

	ch, err := websocket.New(websocket.Options{
		Tokens:   &repoTokenConsumer{repo: repo, pool: pool},
		Replayer: noopReplayer{},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer ch.Close()
	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()

	conn, _, err := codingws.Dial(context.Background(),
		strings.Replace(srv.URL, "http://", "ws://", 1)+websocket.HandlerPath, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(codingws.StatusNormalClosure, "")

	bindMsg := `{"type":"bind","subscriptionId":"` + subID.String() + `","token":"real-token"}`
	if err := conn.Write(context.Background(), codingws.MessageText, []byte(bindMsg)); err != nil {
		t.Fatalf("write bind: %v", err)
	}
	rctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, data, err := conn.Read(rctx)
	if err != nil {
		t.Fatalf("read bind reply: %v", err)
	}
	if !strings.Contains(string(data), `"bind-success"`) {
		t.Fatalf("bind reply = %s", data)
	}

	// Second bind on the same token must fail closed.
	conn2, _, err := codingws.Dial(context.Background(),
		strings.Replace(srv.URL, "http://", "ws://", 1)+websocket.HandlerPath, nil)
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	defer conn2.Close(codingws.StatusNormalClosure, "")
	if err := conn2.Write(context.Background(), codingws.MessageText, []byte(bindMsg)); err != nil {
		t.Fatalf("write bind 2: %v", err)
	}
	rctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	_, data2, err := conn2.Read(rctx2)
	if err != nil {
		t.Fatalf("read bind reply 2: %v", err)
	}
	if !strings.Contains(string(data2), `"bind-error"`) {
		t.Errorf("second bind reply = %s; want bind-error", data2)
	}
}

func TestIntegrationDisconnectMidDeliveryReturnsTransient(t *testing.T) {
	t.Parallel()
	pool := startPostgres(t)

	subID, clientID := seedSubscription(t, pool)
	repo := repos.NewWsBindingTokensRepo()
	if err := repo.Insert(context.Background(), pool, repos.WsBindingTokenRow{
		Token:          "disco-token",
		SubscriptionID: subID,
		ClientID:       clientID,
		ExpiresAt:      time.Now().Add(60 * time.Second),
	}); err != nil {
		t.Fatalf("insert token: %v", err)
	}

	ch, err := websocket.New(websocket.Options{
		Tokens:              &repoTokenConsumer{repo: repo, pool: pool},
		Replayer:            noopReplayer{},
		AckTimeout:          500 * time.Millisecond,
		TransientRetryAfter: 7 * time.Second,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer ch.Close()
	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()

	conn, _, err := codingws.Dial(context.Background(),
		strings.Replace(srv.URL, "http://", "ws://", 1)+websocket.HandlerPath, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := conn.Write(context.Background(), codingws.MessageText,
		[]byte(`{"type":"bind","subscriptionId":"`+subID.String()+`","token":"disco-token"}`)); err != nil {
		t.Fatalf("write bind: %v", err)
	}
	rctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, data, err := conn.Read(rctx)
	cancel()
	if err != nil || !strings.Contains(string(data), `"bind-success"`) {
		t.Fatalf("bind: %v / %s", err, data)
	}

	// Drop the connection from the client side. The next Deliver should
	// classify as Transient (no socket / write fail).
	_ = conn.Close(codingws.StatusNormalClosure, "client disconnect")

	// Give the server's read loop a moment to detect the close and
	// remove the session.
	time.Sleep(100 * time.Millisecond)

	out, err := ch.Deliver(context.Background(), channel.NotificationEnvelope{
		SubscriptionID: subID,
		Sequence:       1,
		BundleBytes:    []byte(`{"r":"x"}`),
		BundleKind:     channel.BundleEventNotification,
		ContentType:    channel.ContentTypeFHIRJSON,
		Deadline:       time.Now().Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if out.Kind != channel.OutcomeTransient {
		t.Errorf("after disconnect outcome = %v %q; want Transient", out.Kind, out.Reason)
	}
	if out.RetryAfter != 7*time.Second {
		t.Errorf("retry-after = %v; want 7s", out.RetryAfter)
	}
}
