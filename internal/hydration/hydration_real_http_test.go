// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package hydration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/hydration"
)

// realHTTPHydrationService is a HydrationService backed by a real
// http.Client dialing a real httptest.NewServer. Used by the story #98
// Phase A tests in compliance with the no-fakes-or-mocks rule:
// hydration must be exercised end-to-end through a real network round
// trip, not an in-memory map. Vendor adapters that override
// BaseHydrationService.Fetch produce something equivalent when wired
// against an EHR FHIR endpoint.
type realHTTPHydrationService struct {
	spi.BaseHydrationService
	client *http.Client
	base   string
}

func newRealHTTPHydrationService(base string) *realHTTPHydrationService {
	return &realHTTPHydrationService{
		client: &http.Client{Timeout: 5 * time.Second},
		base:   strings.TrimRight(base, "/"),
	}
}

func (s *realHTTPHydrationService) Fetch(ctx context.Context, ref spi.FhirReference) (spi.FhirResource, error) {
	url := fmt.Sprintf("%s/%s/%s", s.base, ref.ResourceType, ref.ID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return spi.FhirResource{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/fhir+json")
	resp, err := s.client.Do(req)
	if err != nil {
		return spi.FhirResource{}, fmt.Errorf("fetch %s/%s: %w", ref.ResourceType, ref.ID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return spi.FhirResource{}, fmt.Errorf("fetch %s/%s: status %d", ref.ResourceType, ref.ID, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return spi.FhirResource{}, fmt.Errorf("read %s/%s: %w", ref.ResourceType, ref.ID, err)
	}
	if len(body) == 0 {
		return spi.FhirResource{}, fmt.Errorf("fetch %s/%s: empty body", ref.ResourceType, ref.ID)
	}
	return spi.FhirResource{
		ResourceType: ref.ResourceType,
		ID:           ref.ID,
		Body:         body,
	}, nil
}

// fhirTestServer mounts a minimal FHIR REST surface on httptest.NewServer.
// Routes follow the canonical "/{ResourceType}/{id}" shape so a
// production-shaped HydrationService dials it without rewriting URLs.
// fetchCount tracks GETs so tests can assert hydration depth/cap behavior
// against real HTTP traffic.
type fhirTestServer struct {
	*httptest.Server
	store      map[string][]byte
	fetchCount atomic.Int64
}

func newFHIRTestServer(t *testing.T, store map[string][]byte) *fhirTestServer {
	t.Helper()
	fts := &fhirTestServer{store: store}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// "/Patient/p1" — strip leading slash, split into type+id.
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			http.NotFound(w, r)
			return
		}
		key := parts[0] + "/" + parts[1]
		body, ok := fts.store[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fts.fetchCount.Add(1)
		w.Header().Set("Content-Type", "application/fhir+json")
		_, _ = w.Write(body)
	})
	fts.Server = httptest.NewServer(mux)
	t.Cleanup(fts.Close)
	return fts
}

// TestHydrate_RealHTTPServer_IncludeExpansion (story #98 Phase A AC #1):
// drive the production hydrator against a REAL httptest FHIR server with
// a REAL HTTP-backed HydrationService. An Observation match references
// "subject":"Patient/p1"; hydration must fetch the Patient over HTTP and
// emit it as an Include. Existing in-package tests use an in-memory
// fake — under the no-fakes rule this real-HTTP coverage is required so
// regressions in the production code path can't hide behind stubbed
// fetches.
func TestHydrate_RealHTTPServer_IncludeExpansion(t *testing.T) {
	t.Parallel()

	store := map[string][]byte{
		"Patient/p1": []byte(`{"resourceType":"Patient","id":"p1","name":[{"family":"Smith"}]}`),
	}
	srv := newFHIRTestServer(t, store)
	svc := newRealHTTPHydrationService(srv.URL)

	h := hydration.New(hydration.Config{Service: svc})

	matchBody := []byte(`{"resourceType":"Observation","id":"o1","subject":{"reference":"Patient/p1"}}`)
	matches := []spi.FhirResource{{ResourceType: "Observation", ID: "o1", Body: matchBody}}
	rules := []hydration.IncludeRule{{SourceType: "Observation", Param: "subject"}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := h.Hydrate(ctx, matches, rules)
	if err != nil {
		t.Fatalf("Hydrate against real HTTP server: %v", err)
	}
	if len(res.Include) != 1 {
		t.Fatalf("expected 1 include resource via real HTTP fetch; got %d (warnings=%v)",
			len(res.Include), res.Warnings)
	}
	got := res.Include[0]
	if got.ResourceType != "Patient" || got.ID != "p1" {
		t.Errorf("hydrated include identity wrong: got %s/%s want Patient/p1",
			got.ResourceType, got.ID)
	}
	if !strings.Contains(string(got.Body), `"family":"Smith"`) {
		t.Errorf("hydrated body missing original FHIR JSON content; got %q", string(got.Body))
	}
	if n := srv.fetchCount.Load(); n != 1 {
		t.Errorf("expected exactly 1 real HTTP GET for the include; got %d", n)
	}
}

// TestHydrate_RealHTTPServer_RecursionDepthCap (story #98 Phase A AC #2):
// the hydrator must cap recursion at MaxDepth even when each fetched
// resource itself carries a forward reference. Build a real chain
// Observation -> Encounter (depth 1) -> Patient (depth 2) -> Practitioner
// (would be depth 3) on the FHIR test server, set MaxDepth=2, and assert
// only the depth-1 and depth-2 resources are fetched. The Practitioner
// must NOT be fetched, proving the cap is enforced against real HTTP
// traffic.
func TestHydrate_RealHTTPServer_RecursionDepthCap(t *testing.T) {
	t.Parallel()

	store := map[string][]byte{
		"Encounter/e1":     []byte(`{"resourceType":"Encounter","id":"e1","subject":{"reference":"Patient/p1"}}`),
		"Patient/p1":       []byte(`{"resourceType":"Patient","id":"p1","generalPractitioner":[{"reference":"Practitioner/dr1"}]}`),
		"Practitioner/dr1": []byte(`{"resourceType":"Practitioner","id":"dr1"}`),
	}
	srv := newFHIRTestServer(t, store)
	svc := newRealHTTPHydrationService(srv.URL)

	h := hydration.New(hydration.Config{Service: svc, MaxDepth: 2})

	matchBody := []byte(`{"resourceType":"Observation","id":"o1","encounter":{"reference":"Encounter/e1"}}`)
	matches := []spi.FhirResource{{ResourceType: "Observation", ID: "o1", Body: matchBody}}
	rules := []hydration.IncludeRule{
		{SourceType: "Observation", Param: "encounter"},
		{SourceType: "Encounter", Param: "subject"},
		{SourceType: "Patient", Param: "generalPractitioner"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := h.Hydrate(ctx, matches, rules)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	got := map[string]bool{}
	for _, inc := range res.Include {
		got[inc.ResourceType+"/"+inc.ID] = true
	}
	if !got["Encounter/e1"] || !got["Patient/p1"] {
		t.Errorf("expected Encounter/e1 and Patient/p1 in includes; got %v", got)
	}
	if got["Practitioner/dr1"] {
		t.Errorf("MaxDepth=2 violated: Practitioner/dr1 was fetched at depth 3 (got=%v)", got)
	}
	if n := srv.fetchCount.Load(); n != 2 {
		t.Errorf("expected exactly 2 real HTTP fetches under depth cap; got %d", n)
	}
}

// TestHydrate_RealHTTPServer_TotalResourceCap (story #98 Phase A AC #3):
// MaxResources caps the matched + included count. Stage a match with N
// references against a real FHIR server, set MaxResources=3 (1 match + 2
// includes), and assert the third reference is NOT fetched. A cap warning
// must be emitted.
func TestHydrate_RealHTTPServer_TotalResourceCap(t *testing.T) {
	t.Parallel()

	store := map[string][]byte{
		"Patient/p1": []byte(`{"resourceType":"Patient","id":"p1"}`),
		"Patient/p2": []byte(`{"resourceType":"Patient","id":"p2"}`),
		"Patient/p3": []byte(`{"resourceType":"Patient","id":"p3"}`),
		"Patient/p4": []byte(`{"resourceType":"Patient","id":"p4"}`),
	}
	srv := newFHIRTestServer(t, store)
	svc := newRealHTTPHydrationService(srv.URL)

	h := hydration.New(hydration.Config{Service: svc, MaxResources: 3})

	matchBody := []byte(`{"resourceType":"List","id":"L1","subject":[{"reference":"Patient/p1"},{"reference":"Patient/p2"},{"reference":"Patient/p3"},{"reference":"Patient/p4"}]}`)
	matches := []spi.FhirResource{{ResourceType: "List", ID: "L1", Body: matchBody}}
	rules := []hydration.IncludeRule{{SourceType: "List", Param: "subject"}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := h.Hydrate(ctx, matches, rules)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	total := len(res.Match) + len(res.Include)
	if total > 3 {
		t.Errorf("MaxResources=3 violated; total=%d include=%d", total, len(res.Include))
	}
	if len(res.Include) != 2 {
		t.Errorf("expected exactly 2 includes (1 match + 2 = 3 cap); got %d", len(res.Include))
	}
	hasCapWarning := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "hydration cap") {
			hasCapWarning = true
			break
		}
	}
	if !hasCapWarning {
		t.Errorf("expected a 'hydration cap' warning; got warnings=%v", res.Warnings)
	}
	if n := srv.fetchCount.Load(); n > 2 {
		t.Errorf("real HTTP fetches exceeded the cap; got %d", n)
	}
}

// TestHydrate_RealHTTPServer_NetworkFailureDegrades (story #98 Phase A
// AC #4): a real network failure (the test server has been closed) must
// degrade per-reference into a Warning, not propagate an error from
// Hydrate. The match is still emitted, and the unreachable include is
// recorded as a warning containing the reference key.
func TestHydrate_RealHTTPServer_NetworkFailureDegrades(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// Close the server BEFORE Hydrate runs, so the HydrationService
	// hits a real "connection refused" against the captured URL. This
	// is a real network failure — not a stubbed error.
	closedURL := srv.URL
	srv.Close()

	svc := newRealHTTPHydrationService(closedURL)
	h := hydration.New(hydration.Config{Service: svc})

	matchBody := []byte(`{"resourceType":"Observation","id":"o1","subject":{"reference":"Patient/p1"}}`)
	matches := []spi.FhirResource{{ResourceType: "Observation", ID: "o1", Body: matchBody}}
	rules := []hydration.IncludeRule{{SourceType: "Observation", Param: "subject"}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := h.Hydrate(ctx, matches, rules)
	if err != nil {
		t.Fatalf("Hydrate must not return an error on per-ref network failure; got %v", err)
	}
	if len(res.Match) != 1 {
		t.Errorf("match should still be emitted; got %d matches", len(res.Match))
	}
	if len(res.Include) != 0 {
		t.Errorf("unreachable include should not appear; got %d includes", len(res.Include))
	}
	if len(res.Warnings) == 0 {
		t.Fatalf("expected a warning recording the network failure; got none")
	}
	hit := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "Patient/p1") {
			hit = true
			break
		}
	}
	if !hit {
		t.Errorf("expected warning to name failing ref Patient/p1; got %v", res.Warnings)
	}
}

// TestHydrate_RealHTTPServer_RevincludeRoundTrip (story #98 Phase A
// AC #5): reverse include must round-trip through a real FHIR search
// endpoint. The default extractor returns no refs for reverse rules,
// so this test wires a custom extractor that issues a real HTTP search
// query against the test server and parses the bundle. This proves the
// production wiring honors revinclude when the adapter provides the
// search-backed extractor — exactly the contract in the hydration
// package doc.
func TestHydrate_RealHTTPServer_RevincludeRoundTrip(t *testing.T) {
	t.Parallel()

	encounterBody := []byte(`{"resourceType":"Encounter","id":"e1","subject":{"reference":"Patient/p1"}}`)
	store := map[string][]byte{
		"Encounter/e1": encounterBody,
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var fetches atomic.Int64
	mux.HandleFunc("/Encounter/", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/")
		if body, ok := store[key]; ok {
			fetches.Add(1)
			w.Header().Set("Content-Type", "application/fhir+json")
			_, _ = w.Write(body)
			return
		}
		http.NotFound(w, r)
	})
	// Real FHIR search: GET /Encounter?subject=Patient/p1
	mux.HandleFunc("/Encounter", func(w http.ResponseWriter, r *http.Request) {
		subject := r.URL.Query().Get("subject")
		bundle := map[string]any{
			"resourceType": "Bundle",
			"type":         "searchset",
			"entry":        []any{},
		}
		if subject == "Patient/p1" {
			var enc map[string]any
			_ = json.Unmarshal(encounterBody, &enc)
			bundle["entry"] = []any{map[string]any{"resource": enc}}
		}
		_ = json.NewEncoder(w).Encode(bundle)
	})

	svc := newRealHTTPHydrationService(srv.URL)

	// Custom extractor: issues a REAL search HTTP call to the test
	// server, parses the searchset bundle, and returns the resolved
	// references.
	searchExtractor := func(res spi.FhirResource, rule hydration.IncludeRule) ([]spi.FhirReference, error) {
		if !rule.Reverse || rule.SourceType != "Encounter" || rule.Param != "subject" {
			return nil, nil
		}
		searchURL := fmt.Sprintf("%s/%s?%s=%s/%s", srv.URL, rule.SourceType,
			rule.Param, res.ResourceType, res.ID)
		req, err := http.NewRequest(http.MethodGet, searchURL, nil)
		if err != nil {
			return nil, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		var bundle struct {
			Entry []struct {
				Resource struct {
					ResourceType string `json:"resourceType"`
					ID           string `json:"id"`
				} `json:"resource"`
			} `json:"entry"`
		}
		if derr := json.NewDecoder(resp.Body).Decode(&bundle); derr != nil {
			return nil, derr
		}
		out := make([]spi.FhirReference, 0, len(bundle.Entry))
		for _, e := range bundle.Entry {
			if e.Resource.ResourceType != "" && e.Resource.ID != "" {
				out = append(out, spi.FhirReference{
					ResourceType: e.Resource.ResourceType,
					ID:           e.Resource.ID,
				})
			}
		}
		return out, nil
	}

	h := hydration.New(hydration.Config{Service: svc, Extractor: searchExtractor})

	matchBody := []byte(`{"resourceType":"Patient","id":"p1"}`)
	matches := []spi.FhirResource{{ResourceType: "Patient", ID: "p1", Body: matchBody}}
	rules := []hydration.IncludeRule{{SourceType: "Encounter", Param: "subject", Reverse: true}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := h.Hydrate(ctx, matches, rules)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if len(res.Include) != 1 || res.Include[0].ResourceType != "Encounter" || res.Include[0].ID != "e1" {
		t.Errorf("expected revinclude to fetch Encounter/e1; got %+v", res.Include)
	}
	if got := fetches.Load(); got != 1 {
		t.Errorf("expected 1 real HTTP fetch for the revincluded Encounter; got %d", got)
	}
}
