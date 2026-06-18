// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/fhir-subscriptions-foss/fhir-subs/e2e/mockehr"
	"github.com/fhir-subscriptions-foss/fhir-subs/e2e/mocksub"
)

// Harness is the per-process e2e fixture. One TestMain wires it up; every
// orchestrator test reads from the package-level `harness` variable.
type Harness struct {
	DB           *pgxpool.Pool
	DBURL        string
	MockEHR      *MockEHRHandle
	MockSub      *MockSubHandle
	StubListener *StubListener
}

// MockEHRHandle bundles the EHR-side mocks behind one HTTP server.
type MockEHRHandle struct {
	HTTPServer *http.Server
	HTTPAddr   string
	FHIRStore  *mockehr.FHIRStore
	ChangeFeed *mockehr.ChangeFeed
	// ControlPlane is set after StubListener is up, since the control plane
	// needs to know where to send MLLP frames.
	ControlPlane *mockehr.ControlPlane
}

// MockSubHandle wraps the subscriber-side rest-hook receiver. It exposes
// a tiny InjectNotification helper that drops a synthetic delivery into
// the journal — used by helper-level tests that don't go through the
// full pipeline.
type MockSubHandle struct {
	HTTPServer *http.Server
	HTTPAddr   string
	RestHook   *mocksub.RestHookReceiver
	WSClient   *mocksub.WSClient
	SMTP       *mocksub.FakeSMTP
}

// InjectNotification simulates a delivery for tests that don't drive the
// full pipeline. It POSTs into the local rest-hook receiver.
func (m *MockSubHandle) InjectNotification(subID string, body []byte) {
	url := fmt.Sprintf("http://%s/hook/%s", m.HTTPAddr, subID)
	req, _ := http.NewRequest(http.MethodPost, url, bytesReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, _ := http.DefaultClient.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
}

// harness is the package-global instance set by TestMain. nil if setup
// failed (in which case every harness-backed test should t.Skip via the
// requireHarness gate).
var harness *Harness

// harnessSetupErr captures why TestMain could not bring up the harness,
// so individual tests can surface it in their skip message.
var harnessSetupErr error

// TestMain bootstraps Postgres + mocks once for the package, runs the
// tests, and tears everything down. Setup failures are surfaced as a
// per-test SKIP rather than fataling the whole suite — that way the
// scenario tests still parse and report on environments without a
// Docker daemon (typical for some local dev machines), and CI can
// provide one.
func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	h, cleanup, err := setupHarness(ctx)
	if err != nil {
		harnessSetupErr = err
		fmt.Fprintf(os.Stderr, "WARN: orchestrator harness setup failed; tests will SKIP: %v\n", err)
		os.Exit(m.Run())
	}
	harness = h
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// requireHarness is the gate every harness-backed test calls first. It
// either returns the live harness or skips the calling test with a
// message describing why the harness isn't available.
func requireHarness(t *testing.T) *Harness {
	t.Helper()
	if harness == nil {
		if harnessSetupErr != nil {
			t.Skipf("harness unavailable: %v", harnessSetupErr)
		}
		t.Skip("harness unavailable")
	}
	return harness
}

func setupHarness(ctx context.Context) (*Harness, func(), error) {
	h := &Harness{}
	cleanups := []func(){}
	cleanup := func() {
		// Run cleanups in reverse order.
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}

	// 1) Postgres via testcontainers.
	pgCtr, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("fhirsubs"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("start postgres container: %w", err)
	}
	cleanups = append(cleanups, func() {
		stopCtx, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		_ = pgCtr.Terminate(stopCtx)
	})

	dbURL, err := pgCtr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("get connection string: %w", err)
	}
	h.DBURL = dbURL

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("open pgxpool: %w", err)
	}
	cleanups = append(cleanups, pool.Close)
	h.DB = pool

	// 2) Apply migrations/0001_init.sql against the container.
	if err := applyMigrations(ctx, pool); err != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("apply migrations: %w", err)
	}

	// 3) Stand up mocksub (rest-hook receiver + SMTP) on ephemeral ports.
	subRest := mocksub.NewRestHookReceiver()
	subL, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cleanup()
		return nil, cleanup, err
	}
	subSrv := &http.Server{Handler: subRest.Handler()}
	go func() { _ = subSrv.Serve(subL) }()
	cleanups = append(cleanups, func() {
		stopCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = subSrv.Shutdown(stopCtx)
	})

	smtpSrv, err := mocksub.StartFakeSMTP("127.0.0.1:0")
	if err != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("start fake smtp: %w", err)
	}
	cleanups = append(cleanups, func() { _ = smtpSrv.Close() })

	h.MockSub = &MockSubHandle{
		HTTPServer: subSrv,
		HTTPAddr:   subL.Addr().String(),
		RestHook:   subRest,
		SMTP:       smtpSrv,
	}

	// 4) Stand up the stub MLLP listener (the v1 stand-in for fhir-subs'
	// real listener). Persistence-then-ACK behavior is implemented locally
	// against the testcontainer'd Postgres — this is what the smoke
	// scenarios assert against.
	stub, err := startStubMLLPListener(ctx, h.DB)
	if err != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("start stub MLLP listener: %w", err)
	}
	cleanups = append(cleanups, func() { _ = stub.Close() })
	h.StubListener = stub

	// 5) Stand up mockehr (FHIR store + change feed + scenario control plane).
	fhirStore := mockehr.NewFHIRStore()
	changeFeed := mockehr.NewChangeFeed()
	cp := mockehr.NewControlPlane(mockehr.ControlPlaneConfig{
		MLLPTarget: stub.Addr().String(),
	})

	mux := http.NewServeMux()
	// Ordering matters: longer paths first.
	mux.Handle("/scenarios/", cp.Handler())
	mux.Handle("/change-feed", changeFeed.Handler())
	mux.Handle("/", fhirStore.Handler())

	ehrL, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cleanup()
		return nil, cleanup, err
	}
	ehrSrv := &http.Server{Handler: mux}
	go func() { _ = ehrSrv.Serve(ehrL) }()
	cleanups = append(cleanups, func() {
		stopCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = ehrSrv.Shutdown(stopCtx)
	})

	h.MockEHR = &MockEHRHandle{
		HTTPServer:   ehrSrv,
		HTTPAddr:     ehrL.Addr().String(),
		FHIRStore:    fhirStore,
		ChangeFeed:   changeFeed,
		ControlPlane: cp,
	}

	return h, cleanup, nil
}

// applyMigrations reads migrations/0001_init.sql from the repo root and
// executes it against the pool. Retries pool connection up to 30s to
// tolerate slow port-forwarder establishment on macOS Docker hosts.
func applyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	migrationPath := filepath.Join(root, "migrations", "0001_init.sql")
	body, err := os.ReadFile(migrationPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", migrationPath, err)
	}

	deadline := time.Now().Add(30 * time.Second)
	var pingErr error
	for time.Now().Before(deadline) {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		pingErr = pool.Ping(pingCtx)
		cancel()
		if pingErr == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if pingErr != nil {
		return fmt.Errorf("waiting for postgres: %w", pingErr)
	}

	if _, err := pool.Exec(ctx, string(body)); err != nil {
		return fmt.Errorf("apply %s: %w", migrationPath, err)
	}
	return nil
}

// repoRoot walks up from the orchestrator package until it finds go.mod.
func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for d := wd; d != "/" && d != ""; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d, nil
		}
	}
	return "", errors.New("repo root (go.mod) not found from " + wd)
}

// InsertResourceChange writes a row directly to resource_changes for the
// helper-level tests. Returns the correlation id (uuid string).
func (h *Harness) InsertResourceChange(ctx context.Context,
	adapterID, resourceType, changeKind string, body []byte) (string, error) {
	corrID := uuid.NewString()
	_, err := h.DB.Exec(ctx, `
		insert into resource_changes
		  (id, adapter_id, correlation_id, resource_type, change_kind,
		   resource, occurred_at, created_month)
		values
		  (gen_random_uuid(), $1, $2::uuid, $3, $4, $5,
		   now(), date_trunc('month', now())::date)
	`, adapterID, corrID, resourceType, changeKind, body)
	if err != nil {
		return "", err
	}
	return corrID, nil
}
