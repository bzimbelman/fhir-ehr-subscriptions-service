// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

func TestSearchSubscriptions_HappyPath(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	for i := 0; i < 3; i++ {
		_, _ = subs.Insert(context.Background(), repos.SubscriptionRow{
			ClientID:    "client-A",
			Status:      repos.SubActive,
			TopicURL:    "http://example.org/topics/orders",
			ChannelType: "rest-hook",
			Endpoint:    "https://example.org/wh",
			Content:     "id-only",
			MaxCount:    1,
		})
	}
	srv := newTestServer(t, defaultPrincipal(), deps)

	resp, err := http.Get(srv.URL + "/Subscription")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var bundle map[string]any
	_ = json.Unmarshal(body, &bundle)
	if bundle["resourceType"] != "Bundle" {
		t.Errorf("resourceType = %v", bundle["resourceType"])
	}
	if bundle["type"] != "searchset" {
		t.Errorf("type = %v", bundle["type"])
	}
	if total, _ := bundle["total"].(float64); int(total) != 3 {
		t.Errorf("total = %v; want 3", bundle["total"])
	}
}

func TestSearchSubscriptions_EmptyResultSet(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/Subscription")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var bundle map[string]any
	_ = json.Unmarshal(body, &bundle)
	if total, _ := bundle["total"].(float64); int(total) != 0 {
		t.Errorf("total = %v; want 0", bundle["total"])
	}
}

func TestSearchSubscriptions_OnlyOwnSubscriptionsReturned(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	_, _ = subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID: "other", Status: repos.SubActive, TopicURL: "http://example.org/topics/orders",
		ChannelType: "rest-hook", Endpoint: "https://other/wh", Content: "id-only", MaxCount: 1,
	})
	_, _ = subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID: "client-A", Status: repos.SubActive, TopicURL: "http://example.org/topics/orders",
		ChannelType: "rest-hook", Endpoint: "https://mine/wh", Content: "id-only", MaxCount: 1,
	})
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/Subscription")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var bundle map[string]any
	_ = json.Unmarshal(body, &bundle)
	if total, _ := bundle["total"].(float64); int(total) != 1 {
		t.Errorf("total = %v; want 1 (only own subscriptions)", bundle["total"])
	}
}

func TestSearchTopics_ReturnsActiveBundle(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	topics := deps.Topics.(*memTopics)
	topics.rows = append(topics.rows, repos.SubscriptionTopicRow{
		ID: uuid.New(), URL: "http://example.org/topics/results", Version: "1.0.0",
		Status: "active", Source: "builtin",
		Body: []byte(`{"resourceType":"SubscriptionTopic","url":"http://example.org/topics/results","status":"active"}`),
	})
	// add an inactive one
	topics.rows = append(topics.rows, repos.SubscriptionTopicRow{
		ID: uuid.New(), URL: "http://example.org/topics/retired", Version: "1.0.0",
		Status: "retired", Source: "builtin",
		Body: []byte(`{"resourceType":"SubscriptionTopic"}`),
	})

	// Add a body to the seeded one too so it parses.
	topics.rows[0].Body = []byte(`{"resourceType":"SubscriptionTopic","url":"http://example.org/topics/orders"}`)

	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/SubscriptionTopic")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var bundle map[string]any
	_ = json.Unmarshal(body, &bundle)
	if bundle["resourceType"] != "Bundle" {
		t.Errorf("resourceType = %v", bundle["resourceType"])
	}
	// Two active rows — both must surface.
	total, _ := bundle["total"].(float64)
	if int(total) != 2 {
		t.Errorf("total = %v; want 2", bundle["total"])
	}
}

func TestSearchTopics_EmptyCatalog(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	deps.Topics.(*memTopics).rows = nil
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/SubscriptionTopic")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var bundle map[string]any
	_ = json.Unmarshal(body, &bundle)
	if total, _ := bundle["total"].(float64); int(total) != 0 {
		t.Errorf("empty catalog total = %v; want 0", bundle["total"])
	}
}

func TestReadTopic_HappyPath(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	topics := deps.Topics.(*memTopics)
	id := uuid.New()
	topics.rows = []repos.SubscriptionTopicRow{
		{
			ID: id, URL: "http://example.org/topics/x", Version: "1.0.0",
			Status: "active", Source: "builtin",
			Body: []byte(`{"resourceType":"SubscriptionTopic","url":"http://example.org/topics/x"}`),
		},
	}
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/SubscriptionTopic/" + id.String())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "SubscriptionTopic") {
		t.Errorf("body should contain SubscriptionTopic; got %s", body)
	}
}

func TestReadTopic_MalformedID_404(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/SubscriptionTopic/not-a-uuid")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d; want 404", resp.StatusCode)
	}
}

func TestReadTopic_NotFound_404(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/SubscriptionTopic/" + uuid.New().String())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d; want 404", resp.StatusCode)
	}
}

func TestGetCapabilityStatement_HappyPath(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/metadata")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var cs map[string]any
	_ = json.Unmarshal(body, &cs)
	if cs["resourceType"] != "CapabilityStatement" {
		t.Errorf("resourceType = %v", cs["resourceType"])
	}
	if cs["fhirVersion"] != "5.0.0" {
		t.Errorf("fhirVersion = %v", cs["fhirVersion"])
	}
	rest, _ := cs["rest"].([]any)
	if len(rest) == 0 {
		t.Errorf("rest should be non-empty")
	}
}

func TestGetCapabilityStatement_AdvertisesEmptyTopicCatalog(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	deps.Topics.(*memTopics).rows = nil
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/metadata")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestOpStatusBulk_ReturnsBundleForMultipleIDs(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	id1, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID: "client-A", Status: repos.SubActive, TopicURL: "http://example.org/topics/orders",
		ChannelType: "rest-hook", Endpoint: "https://example.org/wh", Content: "id-only", MaxCount: 1,
	})
	id2, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID: "client-A", Status: repos.SubActive, TopicURL: "http://example.org/topics/orders",
		ChannelType: "rest-hook", Endpoint: "https://example.org/wh", Content: "id-only", MaxCount: 1,
	})
	srv := newTestServer(t, defaultPrincipal(), deps)
	url := srv.URL + "/Subscription/$status?id=" + id1.String() + "&id=" + id2.String()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.Count(string(body), "SubscriptionStatus") != 2 {
		t.Errorf("expected 2 SubscriptionStatus entries; got %s", body)
	}
}

func TestOpStatusBulk_MissingIDParam_400(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/Subscription/$status")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "OperationOutcome") {
		t.Errorf("body should be OperationOutcome; got %s", body)
	}
}

func TestOpStatusBulk_UnknownIDsAreReportedAsOutcome(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/Subscription/$status?id=" + uuid.New().String())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "OperationOutcome") {
		t.Errorf("expected OperationOutcome for unknown id; got %s", body)
	}
}

func TestOpStatusBulk_MalformedID_ReportedAsOutcome(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/Subscription/$status?id=not-a-uuid")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "OperationOutcome") {
		t.Errorf("expected OperationOutcome for malformed id; got %s", body)
	}
}
