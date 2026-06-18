// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package logging is the structured-logging layer of the observability
// module. It produces JSON (or text, for local dev) one event per line
// via slog and runs every record through a PHI redaction filter before
// the record reaches the sink.
//
// Two redaction rules per LLD §6.2:
//
//  1. Fields named resource, bundle, body, raw, or hl7 are dropped at
//     info and above (the value is replaced with the literal "[redacted]"
//     and the field name is preserved). At debug the same rule applies
//     unless DebugLogPayloads is true.
//  2. The query string of any "endpoint" or "url" field is stripped with
//     a regex that retains scheme/host/path; subscribers' bearer tokens
//     and signed-URL parameters that the architecture's example shows
//     ("https://example.com/webhook?[redacted]") never land in logs.
//
// The redactor is a slog.Handler that wraps a real handler (JSON or
// text). Callers receive a *slog.Logger and use it normally; redaction
// is invisible at the call site.
package logging

import (
	"context"
	"io"
	"log/slog"
	"regexp"
	"strings"
)

// PHIFieldNames lists the operational-log field names the redactor
// strips at info and above per LLD §6.2. Match is case-insensitive
// so attribute keys whose case-shape varies across packages
// ("Resource", "RESOURCE", "patient") all redact the same.
var PHIFieldNames = map[string]struct{}{
	"resource": {},
	"bundle":   {},
	"body":     {},
	"raw":      {},
	"hl7":      {},
	// Expanded per S-14 #7: every common PHI / endpoint-shape field
	// observed across emitters in this codebase. Anything that might
	// carry a patient identifier, FHIR Patient subset, contact info,
	// outbound webhook URL, or message header.
	"patient":     {},
	"payload":     {},
	"dob":         {},
	"birthdate":   {},
	"birth_date":  {},
	"mrn":         {},
	"ssn":         {},
	"npi":         {},
	"webhook":     {},
	"callback":    {},
	"target":      {},
	"name":        {},
	"identifier":  {},
	"phone":       {},
	"address":     {},
	"email":       {},
	"hl7_message": {},
}

// QueryStringTargets lists the field names whose query string is
// stripped (LLD §6.2 explicit list). Case-insensitive match.
var QueryStringTargets = map[string]struct{}{
	"endpoint":     {},
	"url":          {},
	"callback_url": {},
	"webhook_url":  {},
	"href":         {},
}

// RedactedMarker is the literal value substituted for redacted fields.
const RedactedMarker = "[redacted]"

// queryRegex matches the first ? and everything after it in a URL-shaped
// string. The replacement collapses to "?[redacted]".
var queryRegex = regexp.MustCompile(`\?.*$`)

// Options configures the logger.
type Options struct {
	// Sink is where the encoded log lines go. Defaults to os.Stdout when nil
	// is passed to NewLogger; tests pass a buffer.
	Sink io.Writer
	// Level is the minimum slog level emitted.
	Level slog.Level
	// Format is "json" (default) or "text".
	Format string
	// DebugLogPayloads opts in to retaining PHI fields at debug. Default
	// is false — even at debug, PHI is redacted.
	DebugLogPayloads bool
	// OnPHIDropped, when non-nil, is called once per dropped field
	// occurrence with the field's name. The metrics layer hooks this to
	// increment fhir_subs_logging_phi_dropped_total{field}.
	OnPHIDropped func(field string)
}

// correlationIDKey is an internal context key.
type correlationIDKey struct{}

// WithCorrelationID returns a copy of ctx carrying the correlation id.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationIDKey{}, id)
}

// CorrelationIDFromContext returns the correlation id stored in ctx, or
// empty if none.
func CorrelationIDFromContext(ctx context.Context) string {
	if v := ctx.Value(correlationIDKey{}); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// NewLogger constructs a *slog.Logger with the redaction filter installed.
func NewLogger(opts *Options) *slog.Logger {
	if opts == nil {
		opts = &Options{}
	}
	sink := opts.Sink
	if sink == nil {
		sink = io.Discard
	}
	hopts := &slog.HandlerOptions{Level: opts.Level}

	var inner slog.Handler
	if strings.EqualFold(opts.Format, "text") {
		inner = slog.NewTextHandler(sink, hopts)
	} else {
		inner = slog.NewJSONHandler(sink, hopts)
	}

	return slog.New(&redactingHandler{
		inner:            inner,
		debugLogPayloads: opts.DebugLogPayloads,
		onDropped:        opts.OnPHIDropped,
	})
}

// redactingHandler is the slog.Handler middleware that enforces the PHI
// rules and copies correlation_id off the context onto the record.
type redactingHandler struct {
	inner            slog.Handler
	debugLogPayloads bool
	onDropped        func(field string)
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	// Pull correlation_id off the context if present and the record
	// doesn't already carry one.
	if cid := CorrelationIDFromContext(ctx); cid != "" {
		hasCID := false
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "correlation_id" {
				hasCID = true
				return false
			}
			return true
		})
		if !hasCID {
			r.AddAttrs(slog.String("correlation_id", cid))
		}
	}

	level := r.Level
	allowPayloads := level == slog.LevelDebug && h.debugLogPayloads

	// Build a new record with redacted attrs.
	newRec := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		newRec.AddAttrs(h.redactAttr(a, allowPayloads))
		return true
	})
	return h.inner.Handle(ctx, newRec)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		redacted = append(redacted, h.redactAttr(a, false))
	}
	return &redactingHandler{
		inner:            h.inner.WithAttrs(redacted),
		debugLogPayloads: h.debugLogPayloads,
		onDropped:        h.onDropped,
	}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{
		inner:            h.inner.WithGroup(name),
		debugLogPayloads: h.debugLogPayloads,
		onDropped:        h.onDropped,
	}
}

// redactAttr applies the PHI rules to a single attribute. Group attrs
// recurse so nested fields are also redacted.
func (h *redactingHandler) redactAttr(a slog.Attr, allowPayloads bool) slog.Attr {
	if a.Value.Kind() == slog.KindGroup {
		group := a.Value.Group()
		newGroup := make([]slog.Attr, 0, len(group))
		for _, ga := range group {
			newGroup = append(newGroup, h.redactAttr(ga, allowPayloads))
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(newGroup...)}
	}

	keyLC := strings.ToLower(a.Key)
	if _, ok := PHIFieldNames[keyLC]; ok {
		if !allowPayloads {
			if h.onDropped != nil {
				h.onDropped(a.Key)
			}
			return slog.String(a.Key, RedactedMarker)
		}
	}

	if _, ok := QueryStringTargets[keyLC]; ok {
		if a.Value.Kind() == slog.KindString {
			s := a.Value.String()
			if i := strings.Index(s, "?"); i >= 0 {
				redacted := queryRegex.ReplaceAllString(s, "?"+RedactedMarker)
				return slog.String(a.Key, redacted)
			}
		}
	}

	return a
}
