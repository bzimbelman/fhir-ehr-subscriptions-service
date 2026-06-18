// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	hpipe "github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/harness"
)

// TestE2E_S2_SearchSubscriptionsPaginates exercises the runtime API
// against a real Postgres: GET /Subscription returns at most _count
// rows and emits Bundle.link self+next when the result set is larger
// than the page. Following the next link returns the rest with no
// duplicates. (S-2.8)
func TestE2E_S2_SearchSubscriptionsPaginates(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resetPipelineTables(t, ctx, h)

	clientID := "client-s28-page-" + uuid.New().String()[:8]
	api, err := hpipe.StartAPIServer(ctx, hpipe.APIServerConfig{
		Pool:     h.DB,
		ClientID: clientID,
	})
	if err != nil {
		t.Fatalf("api start: %v", err)
	}
	t.Cleanup(func() { _ = api.Close() })

	if err := seedHL7Topic(ctx, h.DB); err != nil {
		t.Fatalf("seed topic: %v", err)
	}

	// Create five subscriptions so we can page across them.
	created := make([]uuid.UUID, 0, 5)
	for i := 0; i < 5; i++ {
		body, _ := json.Marshal(map[string]any{
			"resourceType": "Subscription",
			"status":       "requested",
			"topic":        "http://example.org/topics/hl7-passthrough",
			"channelType":  map[string]any{"code": "rest-hook"},
			"endpoint":     fmt.Sprintf("https://example.org/wh-%d", i),
			"content":      "id-only",
			"channel": map[string]any{
				"type":     "rest-hook",
				"endpoint": fmt.Sprintf("https://example.org/wh-%d", i),
			},
		})
		id, err := hpipe.PostSubscription(ctx, api, http.DefaultClient, body)
		if err != nil {
			t.Fatalf("post %d: %v", i, err)
		}
		created = append(created, id)
		// 1ms gap to keep created_at strictly ordered for a deterministic
		// keyset cursor traversal.
		time.Sleep(2 * time.Millisecond)
	}

	// Page 1: _count=2 → 2 entries + next link.
	page1 := fetchPage(t, ctx, api.URL+"/Subscription?_count=2")
	if got := len(page1.Entries); got != 2 {
		t.Fatalf("page1 entries = %d; want 2", got)
	}
	nextURL := page1.linkHref("next")
	if nextURL == "" {
		t.Fatalf("page1 missing next link; body=%s", page1.RawBody)
	}

	// Page 2: follow cursor.
	page2 := fetchPage(t, ctx, api.URL+nextURL)
	if got := len(page2.Entries); got != 2 {
		t.Fatalf("page2 entries = %d; want 2", got)
	}
	for _, e := range page2.Entries {
		for _, p := range page1.Entries {
			if e.ID == p.ID {
				t.Errorf("page2 contains duplicate id %s already on page1", e.ID)
			}
		}
	}

	// Page 3: should have 1 entry and NO next link (we have 5 total).
	nextURL2 := page2.linkHref("next")
	if nextURL2 == "" {
		t.Fatalf("page2 missing next link; body=%s", page2.RawBody)
	}
	page3 := fetchPage(t, ctx, api.URL+nextURL2)
	if got := len(page3.Entries); got != 1 {
		t.Fatalf("page3 entries = %d; want 1", got)
	}
	if page3.linkHref("next") != "" {
		t.Errorf("final page should not advertise next; body=%s", page3.RawBody)
	}

	// Sanity: every created subscription must appear exactly once.
	seen := map[string]int{}
	for _, p := range []pagedBundle{page1, page2, page3} {
		for _, e := range p.Entries {
			seen[e.ID]++
		}
	}
	for _, id := range created {
		if seen[id.String()] != 1 {
			t.Errorf("expected %s once, saw %d", id, seen[id.String()])
		}
	}
}

// TestE2E_S2_SearchRejectsBadCount asserts the runtime answers 400 to
// non-integer or negative `_count` values. (S-2.8)
func TestE2E_S2_SearchRejectsBadCount(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resetPipelineTables(t, ctx, h)

	clientID := "client-s28-bad-" + uuid.New().String()[:8]
	api, err := hpipe.StartAPIServer(ctx, hpipe.APIServerConfig{
		Pool:     h.DB,
		ClientID: clientID,
	})
	if err != nil {
		t.Fatalf("api start: %v", err)
	}
	t.Cleanup(func() { _ = api.Close() })

	cases := []string{"_count=-3", "_count=abc", "_cursor=not-a-cursor"}
	for _, q := range cases {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, api.URL+"/Subscription?"+q, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET ?%s: %v", q, err)
		}
		bb, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("?%s status = %d; want 400; body=%s", q, resp.StatusCode, bb)
		}
	}
}

// pagedBundle is a minimal projection of a FHIR searchset Bundle for
// pagination assertions.
type pagedBundle struct {
	RawBody []byte
	Entries []pagedEntry
	Links   []map[string]any
}

type pagedEntry struct {
	ID string
}

func (b pagedBundle) linkHref(rel string) string {
	for _, l := range b.Links {
		if r, _ := l["relation"].(string); r == rel {
			s, _ := l["url"].(string)
			return s
		}
	}
	return ""
}

func fetchPage(t *testing.T, ctx context.Context, url string) pagedBundle {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status=%d body=%s", url, resp.StatusCode, body)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode body: %v body=%s", err, body)
	}
	out := pagedBundle{RawBody: body}
	if entries, ok := raw["entry"].([]any); ok {
		for _, e := range entries {
			m, _ := e.(map[string]any)
			res, _ := m["resource"].(map[string]any)
			id, _ := res["id"].(string)
			out.Entries = append(out.Entries, pagedEntry{ID: id})
		}
	}
	if links, ok := raw["link"].([]any); ok {
		for _, l := range links {
			lm, _ := l.(map[string]any)
			out.Links = append(out.Links, lm)
		}
	}
	return out
}

// trim avoids accidental matches against the raw URL when the test
// helper is called with a query that has no leading slash.
func trimPath(u string) string {
	if i := strings.Index(u, "?"); i >= 0 {
		return u[:i]
	}
	return u
}

// TestE2E_S2_EventsReplayBundleLink exercises the runtime API: the
// $events response carries a self link, and when more events exist
// than EventReplayPageSize the response carries a next link advancing
// `eventsSinceNumber` past the last delivered row. The default page
// size is 1000, so the happy-path with a small set must NOT advertise
// next. (S-2.15)
func TestE2E_S2_EventsReplayBundleLink(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resetPipelineTables(t, ctx, h)

	clientID := "client-s215-link-" + uuid.New().String()[:8]
	api, err := hpipe.StartAPIServer(ctx, hpipe.APIServerConfig{
		Pool:     h.DB,
		ClientID: clientID,
	})
	if err != nil {
		t.Fatalf("api start: %v", err)
	}
	t.Cleanup(func() { _ = api.Close() })

	if err := seedHL7Topic(ctx, h.DB); err != nil {
		t.Fatalf("seed topic: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"resourceType": "Subscription",
		"status":       "requested",
		"topic":        "http://example.org/topics/hl7-passthrough",
		"channelType":  map[string]any{"code": "rest-hook"},
		"endpoint":     "https://example.org/wh",
		"content":      "id-only",
		"channel": map[string]any{
			"type":     "rest-hook",
			"endpoint": "https://example.org/wh",
		},
	})
	id, err := hpipe.PostSubscription(ctx, api, http.DefaultClient, body)
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, api.URL+"/Subscription/"+id.String()+"/$events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET $events: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	bb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, bb)
	}
	var got map[string]any
	if err := json.Unmarshal(bb, &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, bb)
	}
	links, _ := got["link"].([]any)
	if len(links) == 0 {
		t.Fatalf("$events response missing Bundle.link; body=%s", bb)
	}
	hasSelf := false
	for _, l := range links {
		m, _ := l.(map[string]any)
		if rel, _ := m["relation"].(string); rel == "self" {
			hasSelf = true
		}
		if rel, _ := m["relation"].(string); rel == "next" {
			t.Errorf("non-truncated $events must not advertise next; body=%s", bb)
		}
	}
	if !hasSelf {
		t.Errorf("$events response missing self link; body=%s", bb)
	}
	_ = trimPath
}
