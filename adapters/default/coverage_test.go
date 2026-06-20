// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package defaultadapter_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	defaultadapter "github.com/bzimbelman/fhir-ehr-subscriptions-service/adapters/default"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

// These tests target the uncovered surface area called out in OP #307:
// realHTTPHydration.Fetch, hydrationService.Fetch, vendorClient.Consume,
// vendorClient.Translate, scanRunner.RunScan, emptyScanIterator.Next, and
// the MapToFHIR parse-fallback branch. No mocks: real httptest.NewServer
// for the FHIR endpoint, real context, real registered adapter.

func TestHydrationServiceStub_FetchReturnsErrHydrationUnsupported(t *testing.T) {
	t.Parallel()
	a := defaultadapter.New()
	svc := a.BuildHydrationService(spi.AdapterContext{})

	got, err := svc.Fetch(context.Background(), spi.FhirReference{ResourceType: "Patient", ID: "x"})
	if err == nil {
		t.Fatal("hydrationService.Fetch returned nil error")
	}
	if !errors.Is(err, defaultadapter.ErrHydrationUnsupported) {
		t.Fatalf("expected ErrHydrationUnsupported, got %v", err)
	}
	if got.ResourceType != "" || got.ID != "" || len(got.Body) != 0 {
		t.Fatalf("expected zero FhirResource, got %+v", got)
	}
}

func TestErrHydrationUnsupported_Error(t *testing.T) {
	t.Parallel()
	msg := defaultadapter.ErrHydrationUnsupported.Error()
	if msg == "" {
		t.Fatal("ErrHydrationUnsupported.Error() returned empty string")
	}
	if !strings.Contains(msg, "hydration") {
		t.Fatalf("expected error message to mention hydration, got %q", msg)
	}
}

func TestRealHTTPHydration_FetchHappyPath(t *testing.T) {
	t.Parallel()
	body := []byte(`{"resourceType":"Patient","id":"123"}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Patient/123" {
			t.Errorf("path = %q, want /Patient/123", r.URL.Path)
		}
		if r.Header.Get("Accept") != "application/fhir+json" {
			t.Errorf("Accept = %q, want application/fhir+json", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "application/fhir+json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	a := defaultadapter.New()
	svc := a.BuildHydrationService(spi.AdapterContext{HydrationFhirBaseURL: srv.URL + "/"})

	got, err := svc.Fetch(context.Background(), spi.FhirReference{ResourceType: "Patient", ID: "123"})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if got.ResourceType != "Patient" || got.ID != "123" {
		t.Fatalf("got %+v, want Patient/123", got)
	}
	if string(got.Body) != string(body) {
		t.Fatalf("body = %q, want %q", got.Body, body)
	}
}

func TestRealHTTPHydration_FetchNon200Status(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	a := defaultadapter.New()
	svc := a.BuildHydrationService(spi.AdapterContext{HydrationFhirBaseURL: srv.URL})

	_, err := svc.Fetch(context.Background(), spi.FhirReference{ResourceType: "Patient", ID: "missing"})
	if err == nil {
		t.Fatal("expected error on 404, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected error to mention 404, got %v", err)
	}
}

func TestRealHTTPHydration_FetchTransportError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	srv.Close() // immediately close so Do() fails

	a := defaultadapter.New()
	svc := a.BuildHydrationService(spi.AdapterContext{HydrationFhirBaseURL: srv.URL})

	_, err := svc.Fetch(context.Background(), spi.FhirReference{ResourceType: "Patient", ID: "1"})
	if err == nil {
		t.Fatal("expected transport error from closed server, got nil")
	}
}

func TestRealHTTPHydration_FetchInvalidURL(t *testing.T) {
	t.Parallel()
	a := defaultadapter.New()
	svc := a.BuildHydrationService(spi.AdapterContext{HydrationFhirBaseURL: "http://invalid host with spaces"})

	_, err := svc.Fetch(context.Background(), spi.FhirReference{ResourceType: "Patient", ID: "1"})
	if err == nil {
		t.Fatal("expected error on invalid URL, got nil")
	}
}

func TestRealHTTPHydration_FetchOversizeBodyTruncated(t *testing.T) {
	t.Parallel()
	// 5 MiB body, the adapter caps reads at 4 MiB.
	bigBody := make([]byte, 5<<20)
	for i := range bigBody {
		bigBody[i] = 'a'
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/fhir+json")
		_, _ = w.Write(bigBody)
	}))
	defer srv.Close()

	a := defaultadapter.New()
	svc := a.BuildHydrationService(spi.AdapterContext{HydrationFhirBaseURL: srv.URL})

	got, err := svc.Fetch(context.Background(), spi.FhirReference{ResourceType: "Patient", ID: "big"})
	if err != nil {
		t.Fatalf("expected truncated read to succeed, got error: %v", err)
	}
	if len(got.Body) != 4<<20 {
		t.Fatalf("body length = %d, want %d (4 MiB cap)", len(got.Body), 4<<20)
	}
}

func TestVendorClient_ConsumeReturnsOnContextCancel(t *testing.T) {
	t.Parallel()
	a := defaultadapter.New()
	vc := a.BuildVendorAPIClient(spi.AdapterContext{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- vc.Consume(ctx, nopEventSink{}, nil)
	}()

	// Give Consume a moment to enter the blocking <-ctx.Done() select.
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Consume after cancel returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Consume did not return within 2s of cancel")
	}
}

func TestVendorClient_TranslatePanics(t *testing.T) {
	t.Parallel()
	a := defaultadapter.New()
	vc := a.BuildVendorAPIClient(spi.AdapterContext{})

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Translate did not panic")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value type = %T, want string", r)
		}
		if !strings.Contains(msg, "unreachable") {
			t.Fatalf("panic message = %q, expected to mention 'unreachable'", msg)
		}
	}()

	_, _ = vc.Translate(spi.VendorRecord{Cursor: []byte("ignored")})
}

func TestScanRunner_RunScanReturnsEmptyIterator(t *testing.T) {
	t.Parallel()
	a := defaultadapter.New()
	sr := a.BuildFhirScanRunner(spi.AdapterContext{})

	if plan := sr.ScanPlan(); plan != nil {
		t.Fatalf("ScanPlan = %v, want nil", plan)
	}

	iter, err := sr.RunScan(context.Background(), spi.ScanTarget{ResourceType: "Patient"})
	if err != nil {
		t.Fatalf("RunScan returned err: %v", err)
	}
	if iter == nil {
		t.Fatal("RunScan returned nil iterator")
	}

	got, ok, err := iter.Next(context.Background())
	if err != nil {
		t.Fatalf("Next returned err: %v", err)
	}
	if ok {
		t.Fatal("Next returned ok=true on empty iterator")
	}
	if got.ResourceType != "" || got.ID != "" || len(got.Body) != 0 {
		t.Fatalf("Next returned non-zero resource: %+v", got)
	}
}

func TestMapToFHIR_ParseFallbackOnNilSegments(t *testing.T) {
	t.Parallel()
	a := defaultadapter.New()
	hp := a.BuildHl7Processor(spi.AdapterContext{})

	// ParsedHL7Message with nil Segments forces MapToFHIR to re-parse Raw.
	// Feed malformed bytes so the re-parse fails -> returns the parser error.
	parsed := spi.ParsedHL7Message{Raw: []byte("not-a-real-hl7-message")}
	_, err := hp.MapToFHIR(parsed, spi.Classification{})
	if err == nil {
		t.Fatal("expected MapToFHIR re-parse error on malformed Raw, got nil")
	}
}

func TestClassify_NilSegmentsFallsThroughToCreate(t *testing.T) {
	t.Parallel()
	a := defaultadapter.New()
	hp := a.BuildHl7Processor(spi.AdapterContext{})

	c, err := hp.Classify(spi.ParsedHL7Message{Raw: []byte("ignored")})
	if err != nil {
		t.Fatalf("Classify returned err: %v", err)
	}
	if c.Kind != spi.ChangeCreate {
		t.Fatalf("Kind = %v, want ChangeCreate (fallback for nil segments)", c.Kind)
	}
}

// nopEventSink satisfies spi.EventSink for tests that exercise vendor
// Consume on the default adapter (which never pushes anything).
type nopEventSink struct{}

func (nopEventSink) Push(_ context.Context, _ spi.VendorRecord) error { return nil }
