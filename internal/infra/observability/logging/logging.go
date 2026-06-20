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
	// OP #204: endpoint-shape fields are full-redacted, not merely
	// query-string-stripped. Subscribers' callback URLs frequently
	// embed PHI in path segments
	// (e.g. /api/webhooks/<patient-uuid>/inbound) — stripping the
	// `?...` left every byte before the `?` intact, leaking PHI to
	// any log shipper that grep'd for `endpoint=`. Drop the value
	// outright at info+; operators who need the URL for debugging
	// run at debug + DebugLogPayloads.
	"endpoint":     {},
	"webhook_url":  {},
	"callback_url": {},
	"href":         {},
}

// QueryStringTargets lists the field names whose query string AND
// path segments are stripped (LLD §6.2 explicit list, extended by
// OP #204). Case-insensitive match. The `url` field — the only
// remaining URL-shaped field that is NOT in PHIFieldNames — keeps
// its scheme://host so an operator can still see which IdP /
// subscriber the deployment was reaching, but the path and query —
// the two places PHI lives in modern REST URLs — are replaced with
// "/[redacted-path]" and "?[redacted]" respectively.
//
// Endpoint-shape entries (endpoint, webhook_url, callback_url,
// href) appear in BOTH PHIFieldNames and this map. The redactor
// consults PHIFieldNames first, so those entries are full-redacted
// before path/query stripping ever fires.
var QueryStringTargets = map[string]struct{}{
	"endpoint":     {},
	"url":          {},
	"callback_url": {},
	"webhook_url":  {},
	"href":         {},
}

// RedactedMarker is the literal value substituted for redacted fields.
const RedactedMarker = "[redacted]"

// RedactedPathMarker replaces a URL's path segment when the field is
// in QueryStringTargets but not in PHIFieldNames (so the host is
// preserved). OP #204.
const RedactedPathMarker = "/[redacted-path]"

// queryRegex matches the first ? and everything after it in a URL-shaped
// string. The replacement collapses to "?[redacted]".
var queryRegex = regexp.MustCompile(`\?.*$`)

// phiValueRegexes are the value-side patterns that flag PHI even
// when the attribute key is not in PHIFieldNames (OP #204). PHI
// rules previously applied only to attribute keys; an emitter that
// stuck an MRN inside a `note` or `diagnostics` field bypassed
// redaction entirely.
//
// Patterns are conservative — false positives over-redact non-PHI
// values, but PHI exfiltration via mis-keyed log fields is a worse
// failure mode than over-redaction.
var phiValueRegexes = []*regexp.Regexp{
	// SSN: literal NNN-NN-NNNN. Targets the dominant log shape;
	// bare 9-digit runs are too false-positive against counters.
	regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),
	// MRN: 6-12 digit run preceded by a literal "MRN" (any case)
	// and an optional separator. Targets `mrn=12345678`,
	// `MRN: 12345678`, `mrn#12345678`. Bare digit runs are not
	// flagged.
	regexp.MustCompile(`(?i)mrn[:\s=#-]*\d{6,12}`),
	// DoB: YYYY-MM-DD anywhere in the string. Won't false-positive
	// on log timestamps because slog renders those under the `time`
	// key as a slog.Time, not a string attribute, and never reaches
	// the redactor's value walker.
	regexp.MustCompile(`\b(19|20)\d{2}-(0[1-9]|1[0-2])-(0[1-9]|[12]\d|3[01])\b`),
}

// redactPHIInValue runs the value-side regex sweep and returns the
// scrubbed string. Each match is replaced with RedactedMarker; the
// surrounding text is preserved so an operator can still correlate
// the log line by other fields.
func redactPHIInValue(s string) string {
	for _, re := range phiValueRegexes {
		s = re.ReplaceAllString(s, RedactedMarker)
	}
	return s
}

// stripURLPathAndQuery preserves the scheme://host and replaces the
// path with "/[redacted-path]" and the query with "?[redacted]" if
// either is present. Inputs that are not URL-shaped (no "://") are
// returned unchanged so non-URL strings in QueryStringTargets-keyed
// fields don't get mangled.
func stripURLPathAndQuery(s string) string {
	idx := strings.Index(s, "://")
	if idx < 0 {
		// Non-URL value; fall back to query-only strip.
		if i := strings.Index(s, "?"); i >= 0 {
			return queryRegex.ReplaceAllString(s, "?"+RedactedMarker)
		}
		return s
	}
	// Find the first '/' AFTER the "://" — that's the start of the
	// path. Anything before is scheme://host[:port].
	rest := s[idx+3:]
	pathStart := strings.IndexAny(rest, "/?")
	if pathStart < 0 {
		// No path or query.
		return s
	}
	host := s[:idx+3+pathStart]
	tail := rest[pathStart:]
	queryIdx := strings.Index(tail, "?")
	switch {
	case queryIdx == 0:
		// Query but no path.
		return host + "?" + RedactedMarker
	case queryIdx > 0:
		// Path AND query.
		return host + RedactedPathMarker + "?" + RedactedMarker
	default:
		// Path, no query.
		return host + RedactedPathMarker
	}
}

// Options configures the logger.
type Options struct {
	// Sink is where the encoded log lines go. Defaults to os.Stdout when nil
	// is passed to NewLogger; tests pass a buffer.
	Sink io.Writer
	// Level is the minimum slog level emitted. Ignored when LevelVar
	// is non-nil; that takes precedence so callers who want live level
	// changes can swap it under a SIGHUP-driven config reload (story
	// #151).
	Level slog.Level
	// LevelVar, when non-nil, is the live level source; it implements
	// slog.Leveler so the underlying handler reads the current value
	// on every record. Story #151.
	LevelVar *slog.LevelVar
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
	var leveler slog.Leveler = opts.Level
	if opts.LevelVar != nil {
		leveler = opts.LevelVar
	}
	hopts := &slog.HandlerOptions{Level: leveler}

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
			// OP #204: strip BOTH path segments and query string —
			// the previous query-only strip leaked PHI embedded in
			// path segments (e.g. /api/webhooks/<patient-uuid>/inbound).
			redacted := stripURLPathAndQuery(a.Value.String())
			return slog.String(a.Key, redacted)
		}
	}

	// OP #204: value-side PHI scan. Even when the attribute key is
	// not on the PHI list, a string value containing an MRN-, SSN-,
	// or DoB-shaped substring is scrubbed in place. Skip when the
	// caller opted into payload retention (debug + DebugLogPayloads).
	if !allowPayloads && a.Value.Kind() == slog.KindString {
		original := a.Value.String()
		scrubbed := redactPHIInValue(original)
		if scrubbed != original {
			if h.onDropped != nil {
				h.onDropped(a.Key)
			}
			return slog.String(a.Key, scrubbed)
		}
	}

	return a
}
