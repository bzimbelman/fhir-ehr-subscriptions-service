// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// recordingHandlerMetrics captures handler-emitted metrics.
type recordingHandlerMetrics struct {
	mu                  sync.Mutex
	created             int
	updated             int
	deleted             int
	wsTokens            int
	validations         map[string]int
	activatePanicsTotal int
}

func newRecordingHandlerMetrics() *recordingHandlerMetrics {
	return &recordingHandlerMetrics{validations: map[string]int{}}
}

func (m *recordingHandlerMetrics) RecordSubscriptionCreated() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.created++
}
func (m *recordingHandlerMetrics) RecordSubscriptionUpdated() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updated++
}
func (m *recordingHandlerMetrics) RecordSubscriptionDeleted() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted++
}
func (m *recordingHandlerMetrics) RecordWSBindingTokenIssued() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wsTokens++
}
func (m *recordingHandlerMetrics) RecordValidationFailure(kind string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.validations[kind]++
}
func (m *recordingHandlerMetrics) RecordActivatePanic() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activatePanicsTotal++
}

func (m *recordingHandlerMetrics) activatePanics() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activatePanicsTotal
}

func depsWithMetrics(t *testing.T, m handlers.MetricsRecorder) handlers.Deps {
	t.Helper()
	d := defaultDeps(t)
	d.Metrics = m
	return d
}

func TestCreate_RecordsCreatedMetric(t *testing.T) {
	t.Parallel()
	rec := newRecordingHandlerMetrics()
	deps := depsWithMetrics(t, rec)
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
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if rec.created != 1 {
		t.Errorf("created = %d; want 1", rec.created)
	}
}

func TestCreate_BadSchema_RecordsValidationFailureSchema(t *testing.T) {
	t.Parallel()
	rec := newRecordingHandlerMetrics()
	deps := depsWithMetrics(t, rec)
	srv := newTestServer(t, defaultPrincipal(), deps)

	// resourceType wrong → schema validation fails.
	body := `{"resourceType": "WrongType"}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/Subscription", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	if rec.validations["schema"] != 1 {
		t.Errorf("validations{kind=schema} = %d; want 1", rec.validations["schema"])
	}
}

func TestCreate_UnknownTopic_RecordsValidationFailureSemantic(t *testing.T) {
	t.Parallel()
	rec := newRecordingHandlerMetrics()
	deps := depsWithMetrics(t, rec)
	srv := newTestServer(t, defaultPrincipal(), deps)

	body := `{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/unknown",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.org/wh",
		"channel": {"type": "rest-hook"}
	}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/Subscription", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if rec.validations["semantic"] != 1 {
		t.Errorf("validations{kind=semantic} = %d; want 1", rec.validations["semantic"])
	}
}

func TestUpdate_RecordsUpdatedMetric(t *testing.T) {
	t.Parallel()
	rec := newRecordingHandlerMetrics()
	deps := depsWithMetrics(t, rec)
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
		"maxCount": 5,
		"channel": {"type": "rest-hook"}
	}`
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/Subscription/"+id.String(), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	if rec.updated != 1 {
		t.Errorf("updated = %d; want 1", rec.updated)
	}
}

func TestDelete_RecordsDeletedMetric(t *testing.T) {
	t.Parallel()
	rec := newRecordingHandlerMetrics()
	deps := depsWithMetrics(t, rec)
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
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/Subscription/"+id.String(), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if rec.deleted != 1 {
		t.Errorf("deleted = %d; want 1", rec.deleted)
	}
}

func TestGetWsBindingToken_RecordsIssuedMetric(t *testing.T) {
	t.Parallel()
	rec := newRecordingHandlerMetrics()
	deps := depsWithMetrics(t, rec)
	subs := deps.Subscriptions.(*memSubs)
	id, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "websocket",
		Content:     "id-only",
		MaxCount:    1,
	})
	srv := newTestServer(t, defaultPrincipal(), deps)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/Subscription/"+id.String()+"/$get-ws-binding-token", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if rec.wsTokens != 1 {
		t.Errorf("wsTokens = %d; want 1", rec.wsTokens)
	}
}

// keep uuid linter happy
var _ = uuid.Nil
