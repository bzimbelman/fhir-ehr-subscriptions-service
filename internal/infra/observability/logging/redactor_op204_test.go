// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package logging_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/logging"
)

// TestRedactor_OP204_EndpointShapeFieldsFullRedacted covers OP #204
// AC #1: endpoint, webhook_url, callback_url, href fields full-redact
// at info+. The previous redactor stripped the query string only,
// which left embedded PHI in the path (the dominant FHIR-subscriber
// log shape) intact.
func TestRedactor_OP204_EndpointShapeFieldsFullRedacted(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"endpoint", "webhook_url", "callback_url", "href"} {
		key := key
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			lg := logging.NewLogger(&logging.Options{
				Sink:   &lockedWriter{w: &buf},
				Level:  slog.LevelInfo,
				Format: "json",
			})
			// PHI in BOTH path and query.
			lg.Info("delivery", slog.String(key,
				"https://subscriber.example.org/webhooks/PATIENT-MRN-12345678/inbound?token=abc"))
			line := buf.String()
			// The whole value must be replaced with the literal
			// "[redacted]" — no host, no path, no PHI byte
			// surviving anywhere.
			if !strings.Contains(line, `"`+key+`":"[redacted]"`) {
				t.Fatalf("%q must be full-redacted; got: %s", key, line)
			}
			for _, leak := range []string{"PATIENT-MRN-12345678", "subscriber.example.org", "token=abc"} {
				if strings.Contains(line, leak) {
					t.Errorf("PHI fragment %q leaked through %q field: %s", leak, key, line)
				}
			}
		})
	}
}

// TestRedactor_OP204_PathSegmentsStrippedFromURL covers OP #204
// AC #3 for the generic `url` field. The host is preserved (so
// operators see which IdP / subscriber the deployment was reaching)
// but the path is replaced with "/[redacted-path]" because path
// segments commonly carry resource ids that map to PHI.
func TestRedactor_OP204_PathSegmentsStrippedFromURL(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lg := logging.NewLogger(&logging.Options{
		Sink:   &lockedWriter{w: &buf},
		Level:  slog.LevelInfo,
		Format: "json",
	})
	lg.Info("delivery",
		slog.String("url", "https://idp.example.org/realm/users/4f1c-9dde-3a82/profile?secret=topkek"))
	line := buf.String()
	if !strings.Contains(line, "https://idp.example.org") {
		t.Fatalf("scheme/host should be preserved: %s", line)
	}
	if strings.Contains(line, "/realm/users/4f1c-9dde-3a82/profile") {
		t.Errorf("path segments leaked (OP #204): %s", line)
	}
	if strings.Contains(line, "secret=topkek") {
		t.Errorf("query string leaked: %s", line)
	}
	if !strings.Contains(line, "[redacted-path]") {
		t.Errorf("expected [redacted-path] marker on url field: %s", line)
	}
}

// TestRedactor_OP204_ValueSidePHI_MRN covers OP #204 AC #2: MRN-shaped
// substrings inside ANY string-valued attribute (not just keys on
// the PHI list) are scrubbed in place.
func TestRedactor_OP204_ValueSidePHI_MRN(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lg := logging.NewLogger(&logging.Options{
		Sink:   &lockedWriter{w: &buf},
		Level:  slog.LevelInfo,
		Format: "json",
	})
	// `note` is not a PHI field-name, but the value contains an
	// MRN-shaped substring.
	lg.Info("audit", slog.String("note", "lookup completed for MRN: 87654321 ok"))
	if strings.Contains(buf.String(), "87654321") {
		t.Errorf("MRN value leaked through non-PHI key: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "[redacted]") {
		t.Errorf("expected [redacted] marker on value-side scrub: %s", buf.String())
	}
}

// TestRedactor_OP204_ValueSidePHI_SSN covers SSN-shaped substrings.
func TestRedactor_OP204_ValueSidePHI_SSN(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lg := logging.NewLogger(&logging.Options{
		Sink:   &lockedWriter{w: &buf},
		Level:  slog.LevelInfo,
		Format: "json",
	})
	lg.Info("error", slog.String("diagnostics", "validation failure: ssn 123-45-6789 unexpected"))
	if strings.Contains(buf.String(), "123-45-6789") {
		t.Errorf("SSN value leaked: %s", buf.String())
	}
}

// TestRedactor_OP204_ValueSidePHI_DoB covers YYYY-MM-DD birthdates.
// The slog `time` field is a slog.Time, not a string attribute, so
// the regex never sees it; `note` and other string fields do.
func TestRedactor_OP204_ValueSidePHI_DoB(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lg := logging.NewLogger(&logging.Options{
		Sink:   &lockedWriter{w: &buf},
		Level:  slog.LevelInfo,
		Format: "json",
	})
	lg.Info("audit", slog.String("note", "patient born 1985-04-12, scheduled"))
	if strings.Contains(buf.String(), "1985-04-12") {
		t.Errorf("DoB value leaked: %s", buf.String())
	}
}

// TestRedactor_OP204_OnDroppedFiresOnValueScrub covers the metric
// hook: when a value-side scrub fires the OnPHIDropped callback is
// invoked with the field name so operators can alert on the
// fhir_subs_logging_phi_dropped_total counter.
func TestRedactor_OP204_OnDroppedFiresOnValueScrub(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	dropped := []string{}
	lg := logging.NewLogger(&logging.Options{
		Sink:         &lockedWriter{w: &buf},
		Level:        slog.LevelInfo,
		Format:       "json",
		OnPHIDropped: func(field string) { dropped = append(dropped, field) },
	})
	lg.Info("audit", slog.String("note", "ssn 123-45-6789"))
	if len(dropped) != 1 || dropped[0] != "note" {
		t.Errorf("expected OnPHIDropped(\"note\") on value-side scrub; got %v", dropped)
	}
}
