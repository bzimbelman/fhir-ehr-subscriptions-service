// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package submatcher is Stage 3 of the pipeline: it consumes ehr_events
// rows produced by the topic matcher (Stage 2) and fans them out to
// every active subscription on the matched topic whose filterBy clauses
// satisfy the event's resource.
//
// Evaluate is the pure, in-memory entry point: given one ehr_events row
// and a slice of candidate subscriptions, return a FanoutDecision per
// subscription. It does not touch the database, does not allocate
// beyond the result slice and the JSON unmarshal that filter evaluation
// requires, and is safe to call concurrently. The Worker (in
// worker.go) wires Evaluate to the storage layer: claim, fanout, write
// deliveries rows, mark processed — all in one outbox transaction.
//
// String filters fold with ICU root-locale case+accent rules per ADR
// 0010 #4 — same fold chain as the topic matcher. A filterBy clause
// that fails to parse becomes a FanoutEvaluationError so the worker
// knows to skip the subscription for this event without losing it
// silently as a NoMatch.
//
// See docs/low-level-design/subscriptions-engine.md §3.
package submatcher

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/text/cases"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/storage/repos"
)

// EhrEvent is the submatcher's view of one ehr_events row. It carries
// only what fanout evaluation needs; the worker constructs it from the
// repos.EhrEventRow before calling Evaluate. Decoupling the in-memory
// shape from the row shape keeps unit tests free of partition-key,
// codec, and processed-flag bookkeeping.
type EhrEvent struct {
	ID               uuid.UUID
	EventNumber      int64
	TopicURL         string
	ResourceType     string
	ChangeKind       string // "create" | "update" | "delete"
	Focus            string
	Resource         []byte
	PreviousResource []byte
	CorrelationID    uuid.UUID
	OccurredAt       time.Time
	CreatedMonth     time.Time
}

// FanoutDecision tags one subscription's fate for one event. Keep this
// closed so the caller can switch exhaustively in tests and in metrics.
type FanoutDecision int

// FanoutDecision values.
const (
	// FanoutMatch means the subscription's filterBy clauses all
	// satisfy the event resource. The worker writes a deliveries row.
	FanoutMatch FanoutDecision = iota
	// FanoutNoMatch means at least one filterBy clause failed. The
	// subscription is dropped for this event with no audit-worthy
	// effect.
	FanoutNoMatch
	// FanoutAuthRevoked means the spec-mandated delivery-time scope
	// re-check returned Revoked. The worker transitions the
	// subscription to error/off and writes an audit entry. (The
	// pure-evaluation Evaluate does not produce this on its own — the
	// worker layers auth re-check on top of Evaluate's output.)
	FanoutAuthRevoked
	// FanoutEvaluationError means the filterBy could not be parsed or
	// a clause failed at runtime in a way distinguishable from a clean
	// NoMatch. The worker skips this subscription for this event,
	// increments the runtime-errors metric, and continues with other
	// candidates.
	FanoutEvaluationError
)

// String renders the enum for log + test diagnostics.
func (d FanoutDecision) String() string {
	switch d {
	case FanoutMatch:
		return "Match"
	case FanoutNoMatch:
		return "NoMatch"
	case FanoutAuthRevoked:
		return "AuthRevoked"
	case FanoutEvaluationError:
		return "EvaluationError"
	default:
		return "Unknown"
	}
}

// CandidateDecision is one (subscription, decision) pair returned by
// Evaluate. The caller iterates these and writes one deliveries row per
// FanoutMatch.
type CandidateDecision struct {
	Subscription repos.SubscriptionRow
	Decision     FanoutDecision
	// SkipReason is populated for NoMatch and EvaluationError to feed
	// metrics labels and structured logs.
	SkipReason string
}

// filterClause is the wire shape stored in subscriptions.filter_by per
// internal/api/schemas/subscription.schema.json. Every field is
// optional except filterParameter and value (the schema enforces
// that), so this struct is loose on purpose and the parser fails open
// only when the JSON itself is malformed.
type filterClause struct {
	ResourceType    string `json:"resourceType"`
	FilterParameter string `json:"filterParameter"`
	Modifier        string `json:"modifier"`
	Comparator      string `json:"comparator"`
	Value           string `json:"value"`
}

// Evaluate is the pure fanout evaluator. For each candidate it returns
// one CandidateDecision in input order. A nil/empty filterBy is a
// pass-through Match. Any non-empty filterBy that does not decode is an
// EvaluationError; a successfully-decoded filterBy whose clauses do not
// all satisfy is a NoMatch.
func Evaluate(event EhrEvent, candidates []repos.SubscriptionRow) []CandidateDecision {
	out := make([]CandidateDecision, 0, len(candidates))

	var resourceBody map[string]any
	if len(event.Resource) > 0 {
		_ = json.Unmarshal(event.Resource, &resourceBody)
	}

	for _, sub := range candidates {
		if sub.TopicURL != event.TopicURL {
			out = append(out, CandidateDecision{
				Subscription: sub,
				Decision:     FanoutNoMatch,
				SkipReason:   "topic_mismatch",
			})
			continue
		}

		clauses, err := decodeFilterBy(sub.FilterBy)
		if err != nil {
			out = append(out, CandidateDecision{
				Subscription: sub,
				Decision:     FanoutEvaluationError,
				SkipReason:   "filterby_parse: " + err.Error(),
			})
			continue
		}

		if len(clauses) == 0 {
			out = append(out, CandidateDecision{Subscription: sub, Decision: FanoutMatch})
			continue
		}

		matched := true
		var failReason string
		for i := range clauses {
			ok, why := evaluateClause(clauses[i], resourceBody)
			if !ok {
				matched = false
				failReason = why
				break
			}
		}
		if matched {
			out = append(out, CandidateDecision{Subscription: sub, Decision: FanoutMatch})
		} else {
			out = append(out, CandidateDecision{
				Subscription: sub,
				Decision:     FanoutNoMatch,
				SkipReason:   failReason,
			})
		}
	}
	return out
}

// decodeFilterBy unmarshals subscriptions.filter_by. Empty / null /
// nil bytes => no clauses (pass-through). Everything else must decode
// to a JSON array of filterClause objects.
func decodeFilterBy(raw []byte) ([]filterClause, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var clauses []filterClause
	if err := json.Unmarshal(raw, &clauses); err != nil {
		return nil, err
	}
	return clauses, nil
}

// evaluateClause returns (matched, skipReason). The skipReason is
// populated only on the no-match return so callers can label metrics.
func evaluateClause(c filterClause, body map[string]any) (bool, string) {
	values := extractFieldValues(body, c.FilterParameter)

	switch c.Modifier {
	case "missing":
		want := strings.EqualFold(c.Value, "true")
		ok := (len(values) == 0) == want
		if !ok {
			return false, "modifier:missing"
		}
		return true, ""
	case "not":
		for _, v := range values {
			if equalsToken(v, c.Value) {
				return false, "modifier:not (token equal)"
			}
		}
		return true, ""
	case "contains":
		needle := foldICURoot(c.Value)
		for _, v := range values {
			if strings.Contains(foldICURoot(stringify(v)), needle) {
				return true, ""
			}
		}
		return false, "modifier:contains (no fold-match)"
	case "identifier":
		for _, v := range values {
			if matchIdentifier(v, c.Value) {
				return true, ""
			}
		}
		return false, "modifier:identifier (no match)"
	case "":
		// Bare "patient=..." or "subject=...": reference equality.
		// Bare "status=..." / "code=...": token equality.
		// We try both because the field type is intentionally flexible
		// inside a filterClause.
		for _, v := range values {
			if equalsReference(v, c.Value) {
				return true, ""
			}
			if equalsToken(v, c.Value) {
				return true, ""
			}
			if equalsString(v, c.Value) {
				return true, ""
			}
		}
		return false, "no match (unmodified)"
	default:
		// Unsupported modifier (e.g., :in without a preloaded valueset).
		// Per LLD §3 this surfaces as an EvaluationError so the worker
		// can skip and emit a runtime-error metric. The caller treats
		// the special skipReason "evaluation_error" specially.
		return false, "evaluation_error: unsupported modifier " + c.Modifier
	}
}

// extractFieldValues plucks the values for a filterParameter from the
// resource body. Mirrors the matcher's extractFieldValues to keep
// fanout consistent with topic matching.
func extractFieldValues(body map[string]any, param string) []any {
	if body == nil {
		return nil
	}
	switch param {
	case "status":
		if v, ok := body["status"]; ok {
			return []any{v}
		}
	case "subject", "patient":
		if v, ok := body["subject"].(map[string]any); ok {
			if ref, ok := v["reference"].(string); ok {
				return []any{ref}
			}
		}
		if v, ok := body["patient"].(map[string]any); ok {
			if ref, ok := v["reference"].(string); ok {
				return []any{ref}
			}
		}
	case "code":
		if cc, ok := body["code"].(map[string]any); ok {
			if codings, ok := cc["coding"].([]any); ok {
				out := make([]any, 0, len(codings))
				for _, c := range codings {
					out = append(out, c)
				}
				return out
			}
		}
	case "category":
		if arr, ok := body["category"].([]any); ok {
			out := make([]any, 0, len(arr))
			for _, item := range arr {
				if cc, ok := item.(map[string]any); ok {
					if codings, ok := cc["coding"].([]any); ok {
						for _, c := range codings {
							out = append(out, c)
						}
					}
				}
			}
			return out
		}
	case "name":
		if arr, ok := body["name"].([]any); ok {
			out := make([]any, 0, len(arr))
			for _, item := range arr {
				if hn, ok := item.(map[string]any); ok {
					if text, ok := hn["text"].(string); ok && text != "" {
						out = append(out, text)
					}
					if fam, ok := hn["family"].(string); ok && fam != "" {
						out = append(out, fam)
					}
					if given, ok := hn["given"].([]any); ok {
						for _, g := range given {
							if s, ok := g.(string); ok {
								out = append(out, s)
							}
						}
					}
				}
			}
			return out
		}
	default:
		// Untyped fallback: top-level field of the same name.
		if v, ok := body[param]; ok {
			return []any{v}
		}
	}
	return nil
}

func stringify(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case map[string]any:
		sys, _ := x["system"].(string)
		code, _ := x["code"].(string)
		if sys != "" && code != "" {
			return sys + "|" + code
		}
		if code != "" {
			return code
		}
		ref, _ := x["reference"].(string)
		if ref != "" {
			return ref
		}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func equalsToken(v any, value string) bool {
	switch x := v.(type) {
	case string:
		return x == value
	case map[string]any:
		sys, _ := x["system"].(string)
		code, _ := x["code"].(string)
		if strings.Contains(value, "|") {
			parts := strings.SplitN(value, "|", 2)
			return parts[0] == sys && parts[1] == code
		}
		return value == code
	}
	return false
}

func equalsReference(v any, value string) bool {
	if s, ok := v.(string); ok {
		return s == value
	}
	if m, ok := v.(map[string]any); ok {
		if ref, ok := m["reference"].(string); ok {
			return ref == value
		}
	}
	return false
}

func equalsString(v any, value string) bool {
	if s, ok := v.(string); ok {
		return s == value
	}
	return false
}

func matchIdentifier(v any, value string) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	id, ok := m["identifier"].(map[string]any)
	if !ok {
		return false
	}
	idVal, _ := id["value"].(string)
	idSys, _ := id["system"].(string)
	if strings.Contains(value, "|") {
		parts := strings.SplitN(value, "|", 2)
		return parts[0] == idSys && parts[1] == idVal
	}
	return idVal == value
}

// icuFoldChain is the canonical case+accent fold chain pinned by ADR
// 0010 #4. NFD → strip combining marks → fold (lower) → NFC. Same
// chain the topic matcher uses; kept private to avoid an import cycle.
var icuFoldChain = transform.Chain(
	norm.NFD,
	runes.Remove(runes.In(unicode.Mn)),
	cases.Fold(),
	norm.NFC,
)

func foldICURoot(s string) string {
	out, _, err := transform.String(icuFoldChain, s)
	if err != nil {
		return strings.ToLower(s)
	}
	return out
}
