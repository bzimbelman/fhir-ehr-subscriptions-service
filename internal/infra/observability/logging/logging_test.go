// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/logging"
)

// LLD §6.2: at info+ the logger drops fields named resource, bundle, body,
// raw, hl7. The dropped value is replaced with "[redacted]" and the field
// name is preserved so query patterns survive.
func TestPHIRedaction_InfoDropsForbiddenFields(t *testing.T) {
	t.Parallel()
	cases := []string{"resource", "bundle", "body", "raw", "hl7"}
	for _, field := range cases {
		field := field
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			lg := logging.NewLogger(&logging.Options{
				Sink:   &lockedWriter{w: &buf},
				Level:  slog.LevelInfo,
				Format: "json",
			})
			lg.Info("test", slog.String(field, "patient body content"))

			line := buf.String()
			if !strings.Contains(line, "[redacted]") {
				t.Fatalf("expected [redacted] for field %q; got %s", field, line)
			}
			if strings.Contains(line, "patient body content") {
				t.Fatalf("PHI value leaked in: %s", line)
			}
		})
	}
}

// At debug, by default, payloads are still redacted.
func TestPHIRedaction_DebugDefaultStillRedacts(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lg := logging.NewLogger(&logging.Options{
		Sink:   &lockedWriter{w: &buf},
		Level:  slog.LevelDebug,
		Format: "json",
	})
	lg.Debug("test", slog.String("body", "secret"))

	if strings.Contains(buf.String(), "secret") {
		t.Fatalf("debug must redact by default; got %s", buf.String())
	}
}

// LLD §6.2: query strings on endpoint and url fields are redacted.
func TestQueryStringRedaction_EndpointURL(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lg := logging.NewLogger(&logging.Options{
		Sink:   &lockedWriter{w: &buf},
		Level:  slog.LevelInfo,
		Format: "json",
	})

	lg.Info("delivery",
		slog.String("endpoint", "https://example.com/webhook?token=abc&auth=xyz"),
		slog.String("url", "https://other.example.com/path?secret=top"),
	)

	line := buf.String()
	if strings.Contains(line, "token=abc") || strings.Contains(line, "auth=xyz") || strings.Contains(line, "secret=top") {
		t.Fatalf("query string was not redacted: %s", line)
	}
	if !strings.Contains(line, "[redacted]") {
		t.Fatalf("expected [redacted] marker; got: %s", line)
	}
	if !strings.Contains(line, "https://example.com/webhook") {
		t.Fatalf("expected scheme/host/path retained: %s", line)
	}
}

// Required fields per LLD §6.1 are present.
func TestRequiredFieldsPresent(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lg := logging.NewLogger(&logging.Options{
		Sink:   &lockedWriter{w: &buf},
		Level:  slog.LevelInfo,
		Format: "json",
	})
	lg.Info("hello", slog.String("correlation_id", "abc-123"))

	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &got); err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, k := range []string{"time", "level", "msg", "correlation_id"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("missing required field %q in: %v", k, got)
		}
	}
}

// PHI dropped invokes the OnDropped callback with the field name so the
// metrics layer can increment fhir_subs_logging_phi_dropped_total.
func TestOnDroppedCallback(t *testing.T) {
	t.Parallel()
	dropped := []string{}
	var mu sync.Mutex
	var buf bytes.Buffer
	lg := logging.NewLogger(&logging.Options{
		Sink:   &lockedWriter{w: &buf},
		Level:  slog.LevelInfo,
		Format: "json",
		OnPHIDropped: func(field string) {
			mu.Lock()
			dropped = append(dropped, field)
			mu.Unlock()
		},
	})
	lg.Info("test", slog.String("body", "x"), slog.String("hl7", "y"))

	mu.Lock()
	defer mu.Unlock()
	if len(dropped) != 2 {
		t.Fatalf("expected 2 dropped; got %v", dropped)
	}
}

// Nested attribute groups are also redacted.
func TestNestedGroupRedaction(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lg := logging.NewLogger(&logging.Options{
		Sink:   &lockedWriter{w: &buf},
		Level:  slog.LevelInfo,
		Format: "json",
	})

	lg.Info("nested", slog.Group("ctx", slog.String("body", "secret-payload")))

	if strings.Contains(buf.String(), "secret-payload") {
		t.Fatalf("nested PHI leaked: %s", buf.String())
	}
}

// Debug payloads can be opt-in retained when DebugLogPayloads is true.
func TestDebugLogPayloads_OptIn(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lg := logging.NewLogger(&logging.Options{
		Sink:             &lockedWriter{w: &buf},
		Level:            slog.LevelDebug,
		Format:           "json",
		DebugLogPayloads: true,
	})
	lg.Debug("payload", slog.String("body", "kept"))

	if !strings.Contains(buf.String(), "kept") {
		t.Fatalf("expected payload retained at debug w/ opt-in; got %s", buf.String())
	}
}

// Sanity: text format also works.
func TestTextFormat(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lg := logging.NewLogger(&logging.Options{
		Sink:   &lockedWriter{w: &buf},
		Level:  slog.LevelInfo,
		Format: "text",
	})
	lg.Info("hi")
	if buf.Len() == 0 {
		t.Fatalf("expected output")
	}
}

// Context with a correlation id is propagated as a log field.
func TestContextCorrelationID(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lg := logging.NewLogger(&logging.Options{
		Sink:   &lockedWriter{w: &buf},
		Level:  slog.LevelInfo,
		Format: "json",
	})
	ctx := logging.WithCorrelationID(context.Background(), "corr-xyz")
	lg.InfoContext(ctx, "test")
	if !strings.Contains(buf.String(), "corr-xyz") {
		t.Fatalf("expected correlation_id in: %s", buf.String())
	}
}

// lockedWriter serializes writes so concurrent tests are race-safe.
type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (l *lockedWriter) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.String()
}
