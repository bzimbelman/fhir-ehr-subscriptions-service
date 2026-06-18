// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

func seedActiveRow() repos.SubscriptionRow {
	return repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "rest-hook",
		Endpoint:    "https://example.org/wh",
		Content:     "id-only",
		MaxCount:    1,
	}
}

// auditCapture records the canonical body that was passed into Audit.Append.
type auditCapture struct {
	mu       sync.Mutex
	rows     []auditRow
	failNext bool
}

type auditRow struct {
	action    string
	target    string
	outcome   string
	canonical []byte
}

func (a *auditCapture) Append(_ context.Context, action, target, outcome string, _ *uuid.UUID, canonical []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	cp := append([]byte(nil), canonical...)
	a.rows = append(a.rows, auditRow{action: action, target: target, outcome: outcome, canonical: cp})
	return nil
}

func (a *auditCapture) lastByAction(action string) (auditRow, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := len(a.rows) - 1; i >= 0; i-- {
		if a.rows[i].action == action {
			return a.rows[i], true
		}
	}
	return auditRow{}, false
}

func TestRedactSubscriptionForAudit_StripsChannelHeaders(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"resourceType":"Subscription",
		"channel":{"type":"rest-hook","header":["Authorization: Bearer secret123","X-Foo: bar"]}
	}`)
	out, err := handlers.RedactSubscriptionForAudit(body, handlers.AuditRedactConfig{MaxBytes: 4096})
	if err != nil {
		t.Fatalf("redact: %v", err)
	}
	if bytes.Contains(out, []byte("secret123")) {
		t.Errorf("secret leaked in canonical: %s", out)
	}
	if !bytes.Contains(out, []byte(`"[REDACTED]"`)) {
		t.Errorf("missing [REDACTED] placeholder: %s", out)
	}
}

func TestRedactSubscriptionForAudit_StripsTopLevelHeaderArray(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"resourceType":"Subscription",
		"header":["Authorization: Bearer xyz"]
	}`)
	out, err := handlers.RedactSubscriptionForAudit(body, handlers.AuditRedactConfig{MaxBytes: 4096})
	if err != nil {
		t.Fatalf("redact: %v", err)
	}
	if bytes.Contains(out, []byte("xyz")) {
		t.Errorf("secret leaked: %s", out)
	}
}

func TestRedactSubscriptionForAudit_StripsJWTAlikes(t *testing.T) {
	t.Parallel()
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkphbmUgRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	body := []byte(`{"resourceType":"Subscription","channel":{"endpoint":"https://api.example/wh","custom":"` + jwt + `"}}`)
	out, err := handlers.RedactSubscriptionForAudit(body, handlers.AuditRedactConfig{MaxBytes: 4096})
	if err != nil {
		t.Fatalf("redact: %v", err)
	}
	if bytes.Contains(out, []byte("SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c")) {
		t.Errorf("JWT signature leaked: %s", out)
	}
}

func TestRedactSubscriptionForAudit_StripsPEMBlock(t *testing.T) {
	t.Parallel()
	pem := "-----BEGIN PRIVATE KEY-----\\nMIIEvQIBADANBgkq...\\n-----END PRIVATE KEY-----"
	body := []byte(`{"resourceType":"Subscription","channel":{"endpoint":"https://api.example/wh","privateKey":"` + pem + `"}}`)
	out, err := handlers.RedactSubscriptionForAudit(body, handlers.AuditRedactConfig{MaxBytes: 4096})
	if err != nil {
		t.Fatalf("redact: %v", err)
	}
	if bytes.Contains(out, []byte("MIIEvQIBADANBgkq")) {
		t.Errorf("PEM body leaked: %s", out)
	}
}

func TestRedactSubscriptionForAudit_CapsCanonicalSize(t *testing.T) {
	t.Parallel()
	junk := strings.Repeat("a", 200_000)
	body := []byte(`{"resourceType":"Subscription","reason":"` + junk + `"}`)
	out, err := handlers.RedactSubscriptionForAudit(body, handlers.AuditRedactConfig{MaxBytes: 1024})
	if err != nil {
		t.Fatalf("redact: %v", err)
	}
	if len(out) > 1024 {
		t.Errorf("canonical too large: %d > 1024", len(out))
	}
}

func TestRedactSubscriptionForAudit_PreservesNonSecretShape(t *testing.T) {
	t.Parallel()
	body := []byte(`{"resourceType":"Subscription","topic":"http://t.example/o","channel":{"type":"rest-hook"}}`)
	out, err := handlers.RedactSubscriptionForAudit(body, handlers.AuditRedactConfig{MaxBytes: 4096})
	if err != nil {
		t.Fatalf("redact: %v", err)
	}
	var got map[string]any
	if uErr := json.Unmarshal(out, &got); uErr != nil {
		t.Fatalf("output not valid JSON: %v body=%s", uErr, out)
	}
	if got["resourceType"] != "Subscription" {
		t.Errorf("resourceType lost: %v", got["resourceType"])
	}
	if got["topic"] != "http://t.example/o" {
		t.Errorf("topic lost: %v", got["topic"])
	}
}

// Integration: createSubscription must redact the body it passes to
// Audit.Append.
func TestCreate_AuditPayload_IsRedacted(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	cap := &auditCapture{}
	deps.Audit = cap
	srv := newTestServer(t, defaultPrincipal(), deps)

	body := `{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.org/wh",
		"content": "id-only",
		"channel": {"type": "rest-hook", "endpoint": "https://example.org/wh", "header": ["Authorization: Bearer plaintextSecret999"]}
	}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/Subscription", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	row, ok := cap.lastByAction("subscription.create")
	if !ok {
		t.Fatalf("no audit row captured")
	}
	if bytes.Contains(row.canonical, []byte("plaintextSecret999")) {
		t.Fatalf("audit canonical leaked secret: %s", row.canonical)
	}
}

// Integration: updateSubscription must redact the body it passes to
// Audit.Append.
func TestUpdate_AuditPayload_IsRedacted(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	cap := &auditCapture{}
	deps.Audit = cap
	subs := deps.Subscriptions.(*memSubs)
	id, _ := subs.Insert(context.Background(), seedActiveRow())
	srv := newTestServer(t, defaultPrincipal(), deps)

	body := `{
		"resourceType": "Subscription",
		"status": "active",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.org/wh",
		"content": "id-only",
		"channel": {"type": "rest-hook", "endpoint": "https://example.org/wh", "header": ["Authorization: Bearer rotateMeNow"]}
	}`
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/Subscription/"+id.String(), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	row, ok := cap.lastByAction("subscription.update")
	if !ok {
		t.Fatalf("no update audit row")
	}
	if bytes.Contains(row.canonical, []byte("rotateMeNow")) {
		t.Fatalf("audit canonical leaked secret: %s", row.canonical)
	}
}
