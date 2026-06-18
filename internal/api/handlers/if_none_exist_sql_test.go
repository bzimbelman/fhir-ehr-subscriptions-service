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

// S-2.4: If-None-Exist must push the predicate into SQL via
// FindByClientAndCriteria. The handler must NOT call ListByClient (which
// materialises every row owned by the client). For a no-match case the
// store should be queried exactly once with the parsed criteria, and the
// row scan count should not depend on the size of the client's
// subscription set.
//
// Pre-load the client with 50 subscriptions and assert:
//  1. FindByClientAndCriteria is invoked exactly once;
//  2. ListByClient is NOT invoked from the If-None-Exist path;
//  3. The criteria seen by the store carry the parsed header values;
//  4. The endpoint POST returns 201 (no match → create proceeds).
func TestCreate_IfNoneExist_PushesPredicateIntoSQL_NoMatch(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	for i := 0; i < 50; i++ {
		_, _ = subs.Insert(context.Background(), repos.SubscriptionRow{
			ClientID:    "client-A",
			Status:      repos.SubActive,
			TopicURL:    "http://example.org/topics/orders",
			ChannelType: "rest-hook",
			Endpoint:    "https://example.org/wh-existing",
			Content:     "id-only",
			MaxCount:    1,
		})
	}
	subs.resetCounters()

	srv := newTestServer(t, defaultPrincipal(), deps)
	body := `{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.org/wh-fresh",
		"content": "id-only",
		"channel": {"type": "rest-hook"}
	}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/Subscription", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("If-None-Exist", "topic=http://example.org/topics/orders&channelType=rest-hook&endpoint=https://example.org/wh-fresh")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d; want 201; body=%s", resp.StatusCode, respBody)
	}

	if got := subs.findByClientAndCriteriaCalls(); got != 1 {
		t.Errorf("FindByClientAndCriteria invocations = %d; want 1", got)
	}
	if got := subs.listByClientCallsForIfNoneExist(); got != 0 {
		t.Errorf("ListByClient must not be called from If-None-Exist path; got %d", got)
	}
	last := subs.lastCriteria()
	if last == nil {
		t.Fatalf("FindByClientAndCriteria criteria not captured")
	}
	if last.Topic != "http://example.org/topics/orders" {
		t.Errorf("criteria.Topic = %q; want orders topic", last.Topic)
	}
	if last.ChannelType != "rest-hook" {
		t.Errorf("criteria.ChannelType = %q; want rest-hook", last.ChannelType)
	}
	if last.Endpoint != "https://example.org/wh-fresh" {
		t.Errorf("criteria.Endpoint = %q; want wh-fresh", last.Endpoint)
	}
}
