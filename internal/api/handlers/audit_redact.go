// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"encoding/json"
	"regexp"
	"strings"
)

// AuditRedactConfig parameterises RedactSubscriptionForAudit.
type AuditRedactConfig struct {
	// MaxBytes caps the canonical body persisted in audit_log. Zero
	// means DefaultAuditMaxBytes (16 KiB). Sized to keep one row's
	// blob bounded; the action / target / outcome columns carry the
	// query-relevant metadata.
	MaxBytes int
}

// DefaultAuditMaxBytes is the size cap used when AuditRedactConfig.MaxBytes is zero.
const DefaultAuditMaxBytes = 16 * 1024

const redactedPlaceholder = "[REDACTED]"

// secretFieldNames is the case-insensitive set of JSON keys whose
// VALUES are always replaced with [REDACTED]. The set covers the
// fields R5 / R4B Backport Subscriptions can carry that may smuggle
// outbound credentials, plus a few common patterns we don't want
// landing in audit_log.
var secretFieldNames = map[string]struct{}{
	"header":        {},
	"headers":       {},
	"authorization": {},
	"auth":          {},
	"token":         {},
	"accesstoken":   {},
	"refreshtoken":  {},
	"clientsecret":  {},
	"secret":        {},
	"password":      {},
	"apikey":        {},
	"privatekey":    {},
	"key":           {},
}

// jwtRegexp matches the three-segment dot-separated base64url shape
// of a JWT. We require >=20 chars per body / signature segment so we
// don't redact short tokens-that-happen-to-have-dots like "1.2.3".
var jwtRegexp = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}`)

// pemRegexp matches any PEM-armored block. We strip the entire block
// rather than just the body because the headers themselves can
// disclose the key type.
var pemRegexp = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]+-----.*?-----END [A-Z0-9 ]+-----`)

// longBase64Regexp matches base64-shaped runs of >=64 chars. This is
// imperfect — it catches some legitimate identifiers — but the
// audit-log canonical is best-effort; we err on the side of redaction.
var longBase64Regexp = regexp.MustCompile(`[A-Za-z0-9+/_=-]{64,}`)

// RedactSubscriptionForAudit returns a canonical copy of the
// Subscription create/update body suitable to persist in audit_log.
// The output:
//
//   - Replaces values of secret-named JSON fields with [REDACTED].
//   - Scrubs JWT-shape, PEM-block, and long-base64 substrings inside
//     any leftover string scalar.
//   - Caps total bytes at cfg.MaxBytes (or DefaultAuditMaxBytes), with
//     a `"truncated":true` marker so a future reader knows the row was
//     not the whole body.
//
// Unparseable input is returned as a deterministic short marker so
// callers can still safely persist *something* without leaking the raw
// bytes.
func RedactSubscriptionForAudit(body []byte, cfg AuditRedactConfig) ([]byte, error) {
	max := cfg.MaxBytes
	if max <= 0 {
		max = DefaultAuditMaxBytes
	}

	if len(body) == 0 {
		return []byte(`{}`), nil
	}

	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		// Fall back to a raw string-scrub so we don't ship the
		// unparsed payload, but the result is still a valid JSON
		// envelope so audit log readers don't choke.
		scrubbed := scrubString(string(body))
		out, _ := json.Marshal(map[string]any{
			"unparseable": true,
			"redacted":    scrubbed,
		})
		return capBytes(out, max), nil
	}

	redacted := redactValue(doc)
	out, err := json.Marshal(redacted)
	if err != nil {
		return nil, err
	}
	return capBytes(out, max), nil
}

func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, child := range t {
			if isSecretFieldName(k) {
				out[k] = redactedPlaceholder
				continue
			}
			out[k] = redactValue(child)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, child := range t {
			out[i] = redactValue(child)
		}
		return out
	case string:
		return scrubString(t)
	default:
		return v
	}
}

func isSecretFieldName(k string) bool {
	norm := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(k, "-", ""), "_", ""))
	_, ok := secretFieldNames[norm]
	return ok
}

func scrubString(s string) string {
	if jwtRegexp.MatchString(s) {
		s = jwtRegexp.ReplaceAllString(s, redactedPlaceholder)
	}
	if pemRegexp.MatchString(s) {
		s = pemRegexp.ReplaceAllString(s, redactedPlaceholder)
	}
	// Only scrub long-base64 if it doesn't look like a URL or path.
	// We intentionally don't rewrite https://... — that's the endpoint
	// and audit readers need it.
	if longBase64Regexp.MatchString(s) && !strings.Contains(s, "://") {
		s = longBase64Regexp.ReplaceAllString(s, redactedPlaceholder)
	}
	return s
}

func capBytes(b []byte, max int) []byte {
	if len(b) <= max {
		return b
	}
	// Wrap the original in a truncated envelope. We re-marshal to a
	// stable shape and budget the prefix accordingly.
	const envelopeOverhead = 64
	keep := max - envelopeOverhead
	if keep < 0 {
		keep = 0
	}
	if keep > len(b) {
		keep = len(b)
	}
	envelope := map[string]any{
		"truncated": true,
		"prefix":    string(b[:keep]),
	}
	out, _ := json.Marshal(envelope)
	if len(out) > max {
		// Last resort: hard truncate at max with a stable shape.
		out = []byte(`{"truncated":true}`)
	}
	return out
}
