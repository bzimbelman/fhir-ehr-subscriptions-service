// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// OP #221: every te.fail callsite MUST flow its diagnostic through
// diagnosticForReason. The pre-fix codepath had three callsites that
// passed literal English ("method not allowed", "request body too large",
// "could not parse form") and one ("rate limit exceeded") that surfaced
// directly. After the fix, the response diagnostic must equal the value
// produced by diagnosticForReason for the corresponding reason code.

func TestDiagnosticForReason_CoversNewReasonCodes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		reason string
		want   string
	}{
		// Pre-existing reasons must still resolve.
		{"expired", "assertion expired"},
		{"audience", "assertion audience mismatch"},
		{"malformed", "assertion malformed"},
		{"unknown_client", "unknown client"},
		{"replayed_jti", "assertion jti replay"},
		{"signature", "assertion invalid"},
		// New reasons added for OP #221.
		{"method_not_allowed", "method not allowed"},
		{"body_too_large", "request body too large"},
		{"parse_form_failed", "could not parse form"},
		{"unsupported_grant_type", "only client_credentials is supported"},
		{"rate_limited", "rate limit exceeded"},
		{"server_error", "server error"},
	}
	for _, tc := range cases {
		got := diagnosticForReason(tc.reason)
		if got != tc.want {
			t.Errorf("reason=%q: got %q, want %q", tc.reason, got, tc.want)
		}
	}
}

// AC: the diagnostic that reaches the wire must be the enum value
// (i.e., diagnosticForReason(reason)), not a literal English string the
// caller passed alongside.
func TestServeHTTP_AllFailPathsRouteThroughDiagnosticForReason(t *testing.T) {
	t.Parallel()

	type fc struct {
		name       string
		method     string
		body       url.Values
		oversize   bool
		wantStatus int
		wantReason string
		wantDiag   string
	}
	fixedNow := func() time.Time { return time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC) }
	cases := []fc{
		{
			name:       "method_not_allowed",
			method:     http.MethodGet,
			wantStatus: http.StatusMethodNotAllowed,
			wantReason: "method_not_allowed",
			wantDiag:   "method not allowed",
		},
		{
			name:       "body_too_large",
			method:     http.MethodPost,
			oversize:   true,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantReason: "body_too_large",
			wantDiag:   "request body too large",
		},
		{
			name:       "unsupported_grant_type",
			method:     http.MethodPost,
			body:       url.Values{"grant_type": []string{"password"}},
			wantStatus: http.StatusBadRequest,
			wantReason: "unsupported_grant_type",
			wantDiag:   "only client_credentials is supported",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			te, err := NewTokenEndpoint(TokenEndpointConfig{
				Audience:            "https://example.test/token",
				TokenURL:            "https://example.test/token",
				AccessTokenSecret:   []byte("01234567890123456789012345678901"),
				ClientLookup:        emptyClientLookupCovTest{},
				Now:                 fixedNow,
				MaxRequestBodyBytes: 64,
			})
			if err != nil {
				t.Fatalf("NewTokenEndpoint: %v", err)
			}
			rec := newRecordingMetricsCovTest()
			te.cfg.Metrics = rec

			var body io.Reader
			if tc.oversize {
				body = strings.NewReader(strings.Repeat("a", 1024))
			} else if tc.body != nil {
				body = strings.NewReader(tc.body.Encode())
			}
			req := httptest.NewRequest(tc.method, "/token", body)
			if tc.body != nil || tc.oversize {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			w := httptest.NewRecorder()
			te.ServeHTTP(w, req)

			if w.Code != tc.wantStatus {
				t.Fatalf("status=%d, want %d; body=%s", w.Code, tc.wantStatus, w.Body.String())
			}
			if rec.failures[tc.wantReason] != 1 {
				t.Errorf("RecordAuthFailure(%q) not recorded; got=%v", tc.wantReason, rec.failures)
			}
			var oo struct {
				Issue []struct {
					Diagnostics string `json:"diagnostics"`
				} `json:"issue"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &oo); err != nil {
				t.Fatalf("unmarshal OO: %v body=%s", err, w.Body.String())
			}
			if len(oo.Issue) == 0 {
				t.Fatalf("no issues in OperationOutcome: %s", w.Body.String())
			}
			if oo.Issue[0].Diagnostics != tc.wantDiag {
				t.Errorf("diagnostics=%q, want %q (i.e. diagnosticForReason(%q))",
					oo.Issue[0].Diagnostics, tc.wantDiag, tc.wantReason)
			}
		})
	}
}

type emptyClientLookupCovTest struct{}

func (emptyClientLookupCovTest) GetByID(_ context.Context, _ string) (*ClientRecord, error) {
	return nil, nil
}

type recordingMetricsCovTest struct {
	failures map[string]int
}

func newRecordingMetricsCovTest() *recordingMetricsCovTest {
	return &recordingMetricsCovTest{failures: map[string]int{}}
}

func (r *recordingMetricsCovTest) RecordAuthFailure(reason string) {
	r.failures[reason]++
}
func (recordingMetricsCovTest) RecordTokenIssued()               {}
func (recordingMetricsCovTest) RecordJWKSSingleflightCollision() {}
