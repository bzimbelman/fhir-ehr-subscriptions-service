// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// LLD §4.2: If-Match must enforce optimistic concurrency. A stale ETag
// must be rejected with 409 Conflict OperationOutcome.
func TestUpdate_IfMatch_StaleETag_409(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
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
	srv := newTestServer(t, defaultPrincipal(), deps)
	body := `{
		"resourceType": "Subscription",
		"status": "active",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.org/wh",
		"content": "id-only",
		"channel": {"type": "rest-hook"}
	}`
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/Subscription/"+id.String(), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("If-Match", `W/"00000000-0000-0000-0000-000000000000"`) // stale
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d; want 409; body=%s", resp.StatusCode, respBody)
	}
	respBody, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(respBody), "OperationOutcome") {
		t.Errorf("body should be OperationOutcome; got %s", respBody)
	}
}

// LLD §4.2: If-Match with the current ETag must succeed.
func TestUpdate_IfMatch_CurrentETag_200(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
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
	srv := newTestServer(t, defaultPrincipal(), deps)
	body := `{
		"resourceType": "Subscription",
		"status": "active",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.org/wh",
		"content": "id-only",
		"channel": {"type": "rest-hook"}
	}`
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/Subscription/"+id.String(), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("If-Match", `W/"`+id.String()+`"`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d; want 200; body=%s", resp.StatusCode, respBody)
	}
}

// LLD §4.1: If-None-Exist must enforce ambiguous-result detection. When
// the search criteria match an existing subscription the server returns
// 412 Precondition Failed.
func TestCreate_IfNoneExist_DuplicateSubscription_412(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	_, _ = subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "rest-hook",
		Endpoint:    "https://example.org/wh",
		Content:     "id-only",
		MaxCount:    1,
	})
	srv := newTestServer(t, defaultPrincipal(), deps)
	body := `{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.org/wh",
		"content": "id-only",
		"channel": {"type": "rest-hook"}
	}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/Subscription", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("If-None-Exist", "topic=http://example.org/topics/orders&channelType=rest-hook&endpoint=https://example.org/wh")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d; want 412; body=%s", resp.StatusCode, respBody)
	}
	respBody, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(respBody), "OperationOutcome") {
		t.Errorf("body should be OperationOutcome; got %s", respBody)
	}
}

// LLD §4.1: If-None-Exist with no matching subscription must allow the
// create.
func TestCreate_IfNoneExist_NoMatch_201(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	srv := newTestServer(t, defaultPrincipal(), deps)
	body := `{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.org/wh",
		"content": "id-only",
		"channel": {"type": "rest-hook"}
	}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/Subscription", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("If-None-Exist", "topic=http://example.org/topics/orders&channelType=rest-hook&endpoint=https://example.org/different")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d; want 201; body=%s", resp.StatusCode, respBody)
	}
}

// If-None-Exist on an off (tombstoned) subscription must NOT block the
// create — that's the recreate-after-delete path.
func TestCreate_IfNoneExist_TombstoneIgnored(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	_, _ = subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubOff, // tombstone
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "rest-hook",
		Endpoint:    "https://example.org/wh",
		Content:     "id-only",
		MaxCount:    1,
	})
	srv := newTestServer(t, defaultPrincipal(), deps)
	body := `{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.org/wh",
		"content": "id-only",
		"channel": {"type": "rest-hook"}
	}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/Subscription", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("If-None-Exist", "topic=http://example.org/topics/orders&channelType=rest-hook&endpoint=https://example.org/wh")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d; want 201; body=%s", resp.StatusCode, respBody)
	}
}
