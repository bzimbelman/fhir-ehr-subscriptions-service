// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

func TestSearchSubscriptions_Pagination_FirstPageHasNextLink(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	for i := 0; i < 5; i++ {
		_, err := subs.Insert(context.Background(), repos.SubscriptionRow{
			ClientID:    "client-A",
			Status:      repos.SubActive,
			TopicURL:    "http://example.org/topics/orders",
			ChannelType: "rest-hook",
			Endpoint:    fmt.Sprintf("https://example.org/wh-%d", i),
			Content:     "id-only",
			MaxCount:    1,
		})
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/Subscription?_count=2")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("json: %v body=%s", err, body)
	}
	entries, _ := got["entry"].([]any)
	if len(entries) != 2 {
		t.Errorf("entries on first page = %d; want 2 (got %s)", len(entries), body)
	}
	links, _ := got["link"].([]any)
	if len(links) == 0 {
		t.Fatalf("missing Bundle.link; want at least self+next; body=%s", body)
	}
	rels := map[string]string{}
	for _, l := range links {
		m, _ := l.(map[string]any)
		rel, _ := m["relation"].(string)
		ur, _ := m["url"].(string)
		rels[rel] = ur
	}
	if rels["self"] == "" {
		t.Errorf("missing self link; got %v", rels)
	}
	if rels["next"] == "" {
		t.Errorf("missing next link; got %v body=%s", rels, body)
	}
	if !strings.Contains(rels["next"], "_cursor=") {
		t.Errorf("next link should carry _cursor=; got %q", rels["next"])
	}
}

func TestSearchSubscriptions_Pagination_FollowCursorMidStream(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	for i := 0; i < 5; i++ {
		if _, err := subs.Insert(context.Background(), repos.SubscriptionRow{
			ClientID:    "client-A",
			Status:      repos.SubActive,
			TopicURL:    "http://example.org/topics/orders",
			ChannelType: "rest-hook",
			Endpoint:    fmt.Sprintf("https://example.org/wh-%d", i),
			Content:     "id-only",
			MaxCount:    1,
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	srv := newTestServer(t, defaultPrincipal(), deps)

	// First page.
	resp1, err := http.Get(srv.URL + "/Subscription?_count=2")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp1.Body.Close()
	body1, _ := io.ReadAll(resp1.Body)
	var page1 map[string]any
	_ = json.Unmarshal(body1, &page1)
	first := collectIDs(t, page1)
	nextURL := findRel(page1, "next")
	if nextURL == "" {
		t.Fatalf("first page missing next; body=%s", body1)
	}

	// Second page via cursor.
	resp2, err := http.Get(absURLFromTestPath(srv.URL, nextURL))
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("page2 status=%d body=%s", resp2.StatusCode, body2)
	}
	var page2 map[string]any
	_ = json.Unmarshal(body2, &page2)
	second := collectIDs(t, page2)
	if len(second) != 2 {
		t.Errorf("second page len=%d want 2; body=%s", len(second), body2)
	}
	for _, id := range second {
		for _, fid := range first {
			if id == fid {
				t.Errorf("page2 id %s already on page1", id)
			}
		}
	}
}

func TestSearchSubscriptions_Pagination_LastPageHasNoNextLink(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	for i := 0; i < 3; i++ {
		if _, err := subs.Insert(context.Background(), repos.SubscriptionRow{
			ClientID:    "client-A",
			Status:      repos.SubActive,
			TopicURL:    "http://example.org/topics/orders",
			ChannelType: "rest-hook",
			Endpoint:    fmt.Sprintf("https://example.org/wh-%d", i),
			Content:     "id-only",
			MaxCount:    1,
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/Subscription?_count=10")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if findRel(got, "next") != "" {
		t.Errorf("last page should not advertise next; body=%s", body)
	}
	if findRel(got, "self") == "" {
		t.Errorf("last page missing self; body=%s", body)
	}
}

func TestSearchSubscriptions_Pagination_EmptyResultSet(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/Subscription?_count=10")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if findRel(got, "next") != "" {
		t.Errorf("empty page should not advertise next; body=%s", body)
	}
	if entries, _ := got["entry"].([]any); len(entries) != 0 {
		t.Errorf("entries on empty result = %d; want 0", len(entries))
	}
	if total, _ := got["total"].(float64); total != 0 {
		t.Errorf("total = %v; want 0", total)
	}
}

func TestSearchSubscriptions_Pagination_RejectsBadCount(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/Subscription?_count=-3")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", resp.StatusCode)
	}
}

func TestSearchSubscriptions_Pagination_RejectsBadCursor(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/Subscription?_cursor=not-a-real-cursor")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", resp.StatusCode)
	}
}

func TestEvents_ReplayCap_TruncationAdvertisesNext(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	deps.EventReplayPageSize = 3
	subs := deps.Subscriptions.(*memSubs)
	id, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "rest-hook",
		Endpoint:    "https://example.org/wh",
		Content:     "id-only",
		MaxCount:    1,
	})
	events := deps.Events.(*memEvents)
	for i := int64(1); i <= 7; i++ {
		events.rows = append(events.rows, repos.EhrEventRow{
			ClientID:    "client-A",
			EventNumber: i,
			TopicURL:    "http://example.org/topics/orders",
			Focus:       fmt.Sprintf("ServiceRequest/%d", i),
		})
	}
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/Subscription/" + id.String() + "/$events?eventsSinceNumber=1")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if c := strings.Count(string(body), `"eventNumber"`); c != 3 {
		t.Errorf("expected 3 events on first page; got %d body=%s", c, body)
	}
	nextURL := findRel(got, "next")
	if nextURL == "" {
		t.Fatalf("truncated $events response must advertise next; body=%s", body)
	}
	if !strings.Contains(nextURL, "eventsSinceNumber=") {
		t.Errorf("next link should carry eventsSinceNumber=; got %q", nextURL)
	}
}

func TestEvents_ReplayCap_FollowNextReturnsRest(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	deps.EventReplayPageSize = 3
	subs := deps.Subscriptions.(*memSubs)
	id, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "rest-hook",
		Endpoint:    "https://example.org/wh",
		Content:     "id-only",
		MaxCount:    1,
	})
	events := deps.Events.(*memEvents)
	for i := int64(1); i <= 5; i++ {
		events.rows = append(events.rows, repos.EhrEventRow{
			ClientID:    "client-A",
			EventNumber: i,
			TopicURL:    "http://example.org/topics/orders",
			Focus:       fmt.Sprintf("ServiceRequest/%d", i),
		})
	}
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp1, err := http.Get(srv.URL + "/Subscription/" + id.String() + "/$events")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp1.Body.Close()
	body1, _ := io.ReadAll(resp1.Body)
	var p1 map[string]any
	_ = json.Unmarshal(body1, &p1)
	nextURL := findRel(p1, "next")
	if nextURL == "" {
		t.Fatalf("first page missing next; body=%s", body1)
	}
	resp2, err := http.Get(absURLFromTestPath(srv.URL, nextURL))
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("page2 status=%d body=%s", resp2.StatusCode, body2)
	}
	if c := strings.Count(string(body2), `"eventNumber"`); c != 2 {
		t.Errorf("page2 expected 2 events; got %d body=%s", c, body2)
	}
	var p2 map[string]any
	_ = json.Unmarshal(body2, &p2)
	if findRel(p2, "next") != "" {
		t.Errorf("final page should not advertise next; body=%s", body2)
	}
}

func TestEvents_ReplayCap_NoTruncationNoNextLink(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	deps.EventReplayPageSize = 100
	subs := deps.Subscriptions.(*memSubs)
	id, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "rest-hook",
		Endpoint:    "https://example.org/wh",
		Content:     "id-only",
		MaxCount:    1,
	})
	events := deps.Events.(*memEvents)
	events.rows = []repos.EhrEventRow{
		{ClientID: "client-A", EventNumber: 1, TopicURL: "http://example.org/topics/orders", Focus: "ServiceRequest/a"},
		{ClientID: "client-A", EventNumber: 2, TopicURL: "http://example.org/topics/orders", Focus: "ServiceRequest/b"},
	}
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/Subscription/" + id.String() + "/$events")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if findRel(got, "next") != "" {
		t.Errorf("non-truncated reply must not advertise next; body=%s", body)
	}
}

// findRel returns the URL for the first link with a matching `relation`
// in a FHIR Bundle, or empty string.
func findRel(bundle map[string]any, rel string) string {
	links, _ := bundle["link"].([]any)
	for _, l := range links {
		m, _ := l.(map[string]any)
		if r, _ := m["relation"].(string); r == rel {
			u, _ := m["url"].(string)
			return u
		}
	}
	return ""
}

// collectIDs pulls the resource ids out of a searchset Bundle.
func collectIDs(t *testing.T, bundle map[string]any) []string {
	t.Helper()
	out := []string{}
	entries, _ := bundle["entry"].([]any)
	for _, e := range entries {
		m, _ := e.(map[string]any)
		res, _ := m["resource"].(map[string]any)
		if id, ok := res["id"].(string); ok && id != "" {
			out = append(out, id)
		}
	}
	return out
}

// absURLFromTestPath turns a server-relative or absolute Bundle.link.url
// into an absolute URL the test client can fetch from the httptest
// server. The handler emits self/next as path-relative because there is
// no canonical BaseURL during tests, so the client must re-anchor.
func absURLFromTestPath(srvURL, link string) string {
	if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
		return link
	}
	if !strings.HasPrefix(link, "/") {
		link = "/" + link
	}
	return srvURL + link
}
