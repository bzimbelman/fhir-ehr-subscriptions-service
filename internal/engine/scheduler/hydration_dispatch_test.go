//go:build integration

// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package scheduler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	adapterspi "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/builder"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/scheduler"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/topics/catalog"
)

// realHTTPHydrationService is the same shape the cmd/fhir-subs Phase A
// tests use: a HydrationService backed by a real http.Client dialing a
// real httptest.NewServer. Repeating the type here (instead of importing
// the test binary's helper) keeps each test package self-contained and
// avoids cross-cutting test-only code.
type realHTTPHydrationService struct {
	adapterspi.BaseHydrationService
	client *http.Client
	base   string
}

func (s *realHTTPHydrationService) Fetch(ctx context.Context, ref adapterspi.FhirReference) (adapterspi.FhirResource, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		s.base+"/"+ref.ResourceType+"/"+ref.ID, nil)
	if err != nil {
		return adapterspi.FhirResource{}, err
	}
	req.Header.Set("Accept", "application/fhir+json")
	resp, err := s.client.Do(req)
	if err != nil {
		return adapterspi.FhirResource{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return adapterspi.FhirResource{}, &httpStatusError{code: resp.StatusCode}
	}
	body := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			body = append(body, buf[:n]...)
		}
		if rerr != nil {
			break
		}
	}
	return adapterspi.FhirResource{
		ResourceType: ref.ResourceType,
		ID:           ref.ID,
		Body:         body,
	}, nil
}

type httpStatusError struct{ code int }

func (e *httpStatusError) Error() string { return "fhir status " + strings.TrimSpace(http.StatusText(e.code)) }

// TestIntegrationSchedulerInvokesHydratorForFullResource pins story #98
// AC #4: when a subscription's Content is "full-resource", the
// scheduler MUST invoke the wired hydration.Hydrator and the dispatched
// NotificationEnvelope MUST contain the hydrated `_include` bodies on
// top of the focus body. AC #2 (the conditional invocation) is
// exercised in tandem.
//
// The test is no-fakes: a real httptest.NewServer answers the GET for
// the include resource, a real http.Client dials it, the real
// hydration.Hydrator drives extraction, the real scheduler.Worker
// dispatches, and a real channel records the envelope. Only the
// channel.Channel implementation is a test fixture (recordingChannel
// below) — it captures the envelope so we can assert on the bundle
// contents without needing a real rest-hook subscriber.
//
// Note: recordingChannel is a CAPTURE, not a fake — it records what
// the scheduler actually built and dispatched. The hydration code path
// (Worker.dispatchOne → Worker.hydrateForJob → hydration.Hydrate →
// realHTTPHydrationService.Fetch) is exercised end-to-end with real
// dependencies; only the network egress side of the channel.Deliver
// boundary is shortcut.
func TestIntegrationSchedulerInvokesHydratorForFullResource(t *testing.T) {
	t.Parallel()

	// Allow developers without a working testcontainers / Docker reaper
	// (Mac + Rancher Desktop) to point at an external Postgres via
	// TEST_PG_URL, mirroring cmd/fhir-subs's helper. Falls back to a
	// fresh container otherwise.
	var url string
	if u := os.Getenv("TEST_PG_URL"); u != "" {
		url = u
	} else {
		url = startPostgres(t)
	}
	s := newTestStorage(t, url)
	ctx := context.Background()

	// Real FHIR endpoint for the include lookup.
	patientBody := []byte(`{"resourceType":"Patient","id":"p1","name":[{"family":"Smith"}]}`)
	var patientFetches atomic.Int64
	fhirSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.URL.Path, "/") == "Patient/p1" {
			patientFetches.Add(1)
			w.Header().Set("Content-Type", "application/fhir+json")
			_, _ = w.Write(patientBody)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(fhirSrv.Close)

	hydrationSvc := &realHTTPHydrationService{
		client: &http.Client{Timeout: 5 * time.Second},
		base:   fhirSrv.URL,
	}

	// Topic with a single _include rule: ServiceRequest:subject.
	topicURL := "http://example.org/topics/sr-with-subject"
	compiledTopic := &catalog.Topic{
		CanonicalURL: topicURL,
		NotificationShape: catalog.NotificationShape{
			Includes: []string{"ServiceRequest:subject"},
		},
	}
	topicLookup := scheduler.TopicLookup(func(u string) (*catalog.Topic, bool) {
		if u == topicURL {
			return compiledTopic, true
		}
		return nil, false
	})

	// Real channel registry, real recording channel. The WaitGroup
	// fires from Deliver so the test can synchronize on dispatch
	// completion (TickOnce returns after claim commit, but dispatchOne
	// runs in a goroutine).
	reg := scheduler.NewMapRegistry()
	var dispatchWG sync.WaitGroup
	dispatchWG.Add(1)
	rc := &recordingChannel{wg: &dispatchWG}
	reg.Register("rest-hook-recording", rc)

	// Real scheduler with hydration wired.
	w := scheduler.NewWorker(
		s.Pool().Pgx(),
		s.Subscriptions(), s.EhrEvents(), s.Deliveries(), s.DeadLetters(),
		reg,
		builder.New(builder.Config{}),
		scheduler.Config{ClaimBatchSize: 16, Retry: scheduler.RetryConfig{MaxAttempts: 5}},
		scheduler.Options{
			RNG:              scheduler.DeterministicRNG(1),
			HydrationService: hydrationSvc,
			Topics:           topicLookup,
		},
	)

	// Seed a content=full-resource subscription, an EHR event whose
	// focus body carries a "subject" reference to Patient/p1, and a
	// pending delivery row.
	seedAuthClient(t, s, "client-S")
	subID, err := s.Subscriptions().Insert(ctx, s.Pool().Pgx(), repos.SubscriptionRow{
		ClientID:    "client-S",
		Status:      repos.SubActive,
		TopicURL:    topicURL,
		ChannelType: "rest-hook-recording",
		Endpoint:    "https://sub.example.org/notif",
		Content:     "full-resource",
	})
	if err != nil {
		t.Fatalf("insert sub: %v", err)
	}
	corr := uuid.New()
	srBody := []byte(`{"resourceType":"ServiceRequest","id":"abc","status":"active","subject":{"reference":"Patient/p1"}}`)
	ehrID, _, err := s.EhrEvents().Insert(ctx, s.Pool().Pgx(), repos.EhrEventRow{
		TopicURL:         topicURL,
		Focus:            "ServiceRequest/abc",
		ChangeKind:       repos.ChangeCreate,
		Resource:         srBody,
		CorrelationID:    corr,
		OccurredAt:       time.Now().UTC(),
		ResourceChangeID: uuid.New(),
	})
	if err != nil {
		t.Fatalf("insert ehr_events: %v", err)
	}
	if _, err := s.Deliveries().Insert(ctx, s.Pool().Pgx(), repos.DeliveryRow{
		SubscriptionID: subID,
		EhrEventID:     ehrID,
		EventNumber:    1,
		Status:         repos.DeliveryPending,
		Attempts:       0,
		NextAttemptAt:  time.Now().UTC().Add(-time.Second),
		CorrelationID:  corr,
	}); err != nil {
		t.Fatalf("insert deliveries: %v", err)
	}

	processed, err := w.TickOnce(ctx)
	if err != nil {
		t.Fatalf("TickOnce: %v", err)
	}
	if !processed {
		t.Fatal("expected processed=true; the seeded delivery should have fired")
	}

	// Wait for the dispatchOne goroutine to finish; TickOnce returns
	// as soon as the claim commits, so the channel.Deliver call may
	// not have happened yet.
	dispatchDone := make(chan struct{})
	go func() {
		dispatchWG.Wait()
		close(dispatchDone)
	}()
	select {
	case <-dispatchDone:
	case <-time.After(15 * time.Second):
		t.Fatalf("timed out waiting for dispatchOne to call channel.Deliver")
	}

	// AC #4: the hydrator was invoked — exactly one real GET against
	// the FHIR test server for Patient/p1.
	if got := patientFetches.Load(); got != 1 {
		t.Fatalf("expected exactly 1 real HTTP fetch for Patient/p1; got %d. "+
			"This proves the scheduler did NOT call the hydrator for the "+
			"content=full-resource delivery.", got)
	}

	// AC #2: the dispatched envelope's Bundle contains the hydrated
	// Patient resource alongside the focus body.
	if rc.callCount() != 1 {
		t.Fatalf("expected channel.Deliver called once; got %d", rc.callCount())
	}
	env := rc.last()
	if len(env.BundleBytes) == 0 {
		t.Fatalf("envelope BundleBytes is empty; bundle missing")
	}
	var bundle struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if uerr := json.Unmarshal(env.BundleBytes, &bundle); uerr != nil {
		t.Fatalf("decode envelope bundle: %v", uerr)
	}
	if len(bundle.Entry) < 2 {
		t.Fatalf("expected bundle with at least 2 entries (focus + hydrated include); got %d", len(bundle.Entry))
	}
	// One of the entries must be the hydrated Patient/p1 body.
	hasPatient := false
	for _, e := range bundle.Entry {
		if strings.Contains(string(e.Resource), `"family":"Smith"`) &&
			strings.Contains(string(e.Resource), `"resourceType":"Patient"`) {
			hasPatient = true
			break
		}
	}
	if !hasPatient {
		t.Errorf("dispatched bundle missing hydrated Patient/p1 entry; entries=%d", len(bundle.Entry))
	}
}

// recordingChannel captures every Deliver call so the test can assert
// on the envelope the scheduler built. It is NOT a fake: the channel's
// job per the SPI is to emit the envelope to a transport, and the only
// way to verify the hydrated bundle survived dispatch is to record what
// the scheduler handed off. A real rest-hook subscriber would receive
// the same bytes.
type recordingChannel struct {
	mu    sync.Mutex
	calls []channel.NotificationEnvelope
	wg    *sync.WaitGroup
}

func (r *recordingChannel) Deliver(_ context.Context, env channel.NotificationEnvelope) (channel.DeliveryOutcome, error) {
	r.mu.Lock()
	r.calls = append(r.calls, env)
	r.mu.Unlock()
	if r.wg != nil {
		r.wg.Done()
	}
	return channel.Delivered(), nil
}

func (r *recordingChannel) Close() error { return nil }

func (r *recordingChannel) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *recordingChannel) last() channel.NotificationEnvelope {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[len(r.calls)-1]
}

