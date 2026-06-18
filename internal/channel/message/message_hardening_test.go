// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package message_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/message"
)

// S-5 (default http.Client no Timeout): mirror resthook — the default
// client must carry a wall-clock Timeout to prevent header-drip attacks
// from holding workers past their envelope deadline.
func TestMessage_DefaultClientHasTimeout(t *testing.T) {
	t.Parallel()
	ch, err := message.New(message.Options{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	c := ch.HTTPClientForTest()
	if c.Timeout <= 0 {
		t.Fatalf("default client Timeout = %v; want > 0", c.Timeout)
	}
}

// S-5 (TLS / pool knobs): expose MaxIdleConnsPerHost / MaxConnsPerHost /
// TLSMinVersion via Options for parity with resthook.
func TestMessage_TLSAndPoolKnobs(t *testing.T) {
	t.Parallel()
	ch, err := message.New(message.Options{
		MaxIdleConnsPerHost: 4,
		MaxConnsPerHost:     7,
		TLSMinVersion:       tls.VersionTLS13,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	tr := ch.TransportForTest()
	if tr == nil {
		t.Fatalf("transport is nil")
	}
	if tr.MaxIdleConnsPerHost != 4 {
		t.Errorf("MaxIdleConnsPerHost = %d; want 4", tr.MaxIdleConnsPerHost)
	}
	if tr.MaxConnsPerHost != 7 {
		t.Errorf("MaxConnsPerHost = %d; want 7", tr.MaxConnsPerHost)
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.MinVersion != tls.VersionTLS13 {
		t.Errorf("TLSClientConfig.MinVersion not set to TLS 1.3")
	}
}

// S-5 (content-type validated only at delivery time): a non-fhir+json
// content type produced an in-flight PermanentFailure rather than a
// fail-closed Validate at construction time. The channel exposes a
// validator the API layer can call before persisting Subscription.
func TestMessage_ValidateContentTypeAtBoundary(t *testing.T) {
	t.Parallel()
	ch, err := message.New(message.Options{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := ch.ValidateContentType(channel.ContentTypeFHIRJSON); err != nil {
		t.Errorf("fhir+json should be accepted: %v", err)
	}
	if err := ch.ValidateContentType(channel.ContentTypeFHIRXML); err == nil {
		t.Errorf("fhir+xml should be rejected at create time")
	}
}

// S-5 (Bundle.timestamp uses RFC3339): FHIR `instant` expects sub-second
// precision. Switch to RFC3339Nano so the outer Bundle.timestamp is a
// valid `instant`.
func TestMessage_OuterBundleTimestampSubSecond(t *testing.T) {
	t.Parallel()
	var captured []byte
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		captured = buf
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch, err := message.New(message.Options{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	env := newEnvelope(t, srv.URL+"/msg")
	out, err := ch.Deliver(context.Background(), env)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if out.Kind != channel.OutcomeDelivered {
		t.Fatalf("kind = %v; want Delivered", out.Kind)
	}

	var outer struct {
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(captured, &outer); err != nil {
		t.Fatalf("unmarshal outer: %v; raw=%s", err, string(captured))
	}
	if outer.Timestamp == "" {
		t.Fatalf("timestamp empty")
	}
	// Parse as RFC3339Nano — sub-second precision required by FHIR
	// `instant`. RFC3339 (second precision) parses too, so the stronger
	// check is for the presence of '.' fractional seconds.
	if _, err := time.Parse(time.RFC3339Nano, outer.Timestamp); err != nil {
		t.Fatalf("timestamp %q does not parse as RFC3339Nano: %v", outer.Timestamp, err)
	}
	// FHIR `instant` is documented at sub-second precision; assert the
	// fractional seconds component is present.
	if !containsDot(outer.Timestamp) {
		t.Fatalf("timestamp %q lacks fractional seconds; FHIR instant expects sub-second precision", outer.Timestamp)
	}
}

func containsDot(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			return true
		}
	}
	return false
}
