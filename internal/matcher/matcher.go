// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package matcher is Stage 2 of the pipeline: it consumes
// resource_changes rows, evaluates each against the active
// SubscriptionTopic catalog, and writes one ehr_events row per matching
// topic.
//
// The seven-step algorithm — resource_type gate, interaction gate,
// previous criteria, current criteria, combine, fhirPathCriteria,
// emit — lives in Evaluate. The claim/process loop wraps the
// transactional outbox in Worker.
//
// # Supported FHIR search parameters
//
// The matcher's extractFieldValues function understands a closed set of
// FHIR search parameters. The catalog package's SupportedSearchParameters
// list MUST stay in lockstep with the switch in extractFieldValues —
// any topic referencing a parameter outside this list is rejected at
// catalog load time (B-23) so it cannot silently fail to match at run
// time.
//
// Currently supported parameters: status, subject, patient, code,
// category, name, _lastUpdated.
//
// # FHIRPath
//
// runFHIRPath is a *minimal* gate, not a full evaluator. The LLD calls
// for a sandboxed FHIRPath with timeout, traversal limit, and deny-list
// (see docs/future-work.md P1.2). Only `<Resource>.<field>.exists()`
// and `<Resource>.status = '<v>'` are recognized. Every other expression
// returns false (fail-CLOSED) per B-24 — earlier behavior fell through
// to `return true`, silently firing topics with unrecognized FHIRPath
// like `Patient.deceased.empty()` on every change.
package matcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/cases"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/correlation"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/topics/catalog"
)

// ResourceChange is the matcher's input view of one resource_changes
// row. It carries everything the seven-step algorithm needs without
// dragging the whole row shape (with sequence, partition key, etc.)
// into pure-evaluation tests.
type ResourceChange struct {
	ID               uuid.UUID
	CreatedMonth     time.Time
	ResourceType     string
	ChangeKind       string // "create" | "update" | "delete"
	Resource         []byte
	PreviousResource []byte
	EventCode        string
	CorrelationID    uuid.UUID
	OccurredAt       time.Time
}

// Match is one matched (topic, change) pair. Workers convert these into
// ehr_events row inserts.
type Match struct {
	TopicURL              string
	ChangeKind            string
	Focus                 string
	Resource              []byte
	PreviousResource      []byte
	CorrelationID         uuid.UUID
	OccurredAt            time.Time
	NotificationShapeHint []byte
	ResourceChangeID      uuid.UUID
	// MatchKind is "resource" for a resourceTrigger match, "event"
	// for a vendor event-code match.
	MatchKind string
}

// Evaluate runs every active topic against row and returns one Match
// per topic that fired. Resource-type gating is index-pre-applied
// inside the Catalog; this function stays correct even if the index is
// bypassed because each trigger re-checks resource_types.
//
// Evaluate is pure: no I/O, no allocations except the result slice and
// the ICU folding buffers. Idempotent — calling twice on the same
// (cat, row) returns the same set.
func Evaluate(cat *catalog.Catalog, row ResourceChange) []Match {
	if cat == nil {
		return nil
	}
	out := []Match{}

	candidates := cat.ByResourceType(row.ResourceType)
	for _, t := range candidates {
		if matchOneTopic(t, row) {
			out = append(out, buildMatch(t, row, "resource"))
		}
	}

	if row.EventCode != "" {
		for _, t := range cat.ByEventCode(row.EventCode) {
			out = append(out, buildMatch(t, row, "event"))
		}
	}
	return out
}

func matchOneTopic(t *catalog.Topic, row ResourceChange) bool {
	for _, trigger := range t.Triggers {
		if !trigger.ResourceTypes[row.ResourceType] {
			continue
		}
		// supportedInteraction empty → all interactions per spec.
		if len(trigger.Interactions) > 0 && !trigger.Interactions[row.ChangeKind] {
			continue
		}

		// Step 3 — previous-state criteria.
		var prevMatched bool
		switch {
		case trigger.PreviousCriteria == nil:
			prevMatched = true
		case len(row.PreviousResource) == 0:
			prevMatched = false
		default:
			prevMatched = evaluateSearchExpression(trigger.PreviousCriteria, row.PreviousResource)
		}

		// Step 4 — current-state criteria.
		var currMatched bool
		if trigger.CurrentCriteria == nil {
			currMatched = true
		} else {
			currMatched = evaluateSearchExpression(trigger.CurrentCriteria, row.Resource)
		}

		// Step 5 — combine. requireBoth=true is AND; requireBoth=false
		// is OR per the spec, but if only one criterion is given, the
		// absent side trivially matches and AND collapses to "use the
		// present side".
		var combined bool
		switch {
		case trigger.RequireBoth:
			combined = prevMatched && currMatched
		case trigger.PreviousCriteria == nil || trigger.CurrentCriteria == nil:
			combined = prevMatched && currMatched
		default:
			combined = prevMatched || currMatched
		}
		if !combined {
			continue
		}

		// Step 6 — fhirPathCriteria.
		if trigger.FHIRPath != "" {
			if !runFHIRPath(trigger.FHIRPath, row.Resource, row.PreviousResource) {
				continue
			}
		}

		// Step 7 — match.
		return true
	}
	return false
}

func buildMatch(t *catalog.Topic, row ResourceChange, kind string) Match {
	hint, _ := json.Marshal(struct {
		Resource    string   `json:"resource,omitempty"`
		Includes    []string `json:"includes,omitempty"`
		RevIncludes []string `json:"revIncludes,omitempty"`
	}{
		Resource:    t.NotificationShape.Resource,
		Includes:    t.NotificationShape.Includes,
		RevIncludes: t.NotificationShape.RevIncludes,
	})
	return Match{
		TopicURL:              t.CanonicalURL,
		ChangeKind:            row.ChangeKind,
		Focus:                 referenceForResource(row.ResourceType, row.Resource),
		Resource:              row.Resource,
		PreviousResource:      row.PreviousResource,
		CorrelationID:         row.CorrelationID,
		OccurredAt:            row.OccurredAt,
		NotificationShapeHint: hint,
		ResourceChangeID:      row.ID,
		MatchKind:             kind,
	}
}

// referenceForResource builds the FHIR Reference string the spec puts
// in SubscriptionStatus.notificationEvent.focus, e.g. "ServiceRequest/abc".
func referenceForResource(resourceType string, body []byte) string {
	if len(body) == 0 {
		return resourceType
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return resourceType
	}
	if id, _ := m["id"].(string); id != "" {
		return resourceType + "/" + id
	}
	return resourceType
}

// evaluateSearchExpression runs every clause in expr against resource;
// AND across clauses. Returns false if a clause cannot be satisfied.
//
// S-10.1: a malformed resource body silently fails the topic match.
// We notify the reporter (if installed) so wiring can bump a metric;
// the behavior is unchanged (fail-closed) since resuming with a
// malformed body would produce wrong matches.
func evaluateSearchExpression(expr *catalog.SearchExpression, resource []byte) bool {
	if expr == nil {
		return true
	}
	var m map[string]any
	if err := json.Unmarshal(resource, &m); err != nil {
		reportMalformedResource("search_expression", err)
		return false
	}
	for _, c := range expr.Clauses {
		if !evaluateClause(c, m) {
			return false
		}
	}
	return true
}

// evaluateClause is intentionally narrow. Per ADR 0006 + the LLD's
// supported subset, we handle:
//   - token-style equality (status=active), :not, :missing
//   - string :contains (ICU root-locale folded)
//   - reference equality (subject=Patient/123)
//   - date equality with comparator prefix (eq|ne|gt|lt|ge|le)
func evaluateClause(c catalog.SearchClause, body map[string]any) bool {
	values := extractFieldValues(body, c.Parameter)

	switch c.Modifier {
	case "missing":
		want := strings.EqualFold(c.Value, "true")
		return (len(values) == 0) == want
	case "not":
		for _, v := range values {
			if equalsToken(v, c.Value) {
				return false
			}
		}
		return true
	case "contains":
		needle := foldICURoot(c.Value)
		for _, v := range values {
			if strings.Contains(foldICURoot(stringify(v)), needle) {
				return true
			}
		}
		return false
	case "identifier":
		for _, v := range values {
			if matchIdentifier(v, c.Value) {
				return true
			}
		}
		return false
	case "in":
		// :in ValueSet — ValueSet expansion not wired through yet;
		// fail closed so a topic depending on :in does not silently
		// pass. Notify the reporter so wiring can bump a counter
		// (S-10.3).
		reportUnsupportedModifier("in", c.Parameter)
		return false
	case "":
		if cmp, dateVal, ok := parseDateComparator(c.Value); ok {
			for _, v := range values {
				if compareDate(stringify(v), cmp, dateVal) {
					return true
				}
			}
			return false
		}
		for _, v := range values {
			if equalsToken(v, c.Value) {
				return true
			}
			if equalsReference(v, c.Value) {
				return true
			}
			// S-10.2: parity with submatcher.bareClause — string
			// equality (e.g., for `name=John`) was missing here.
			if equalsString(v, c.Value) {
				return true
			}
		}
		return false
	}
	return false
}

// extractFieldValues yields the values for a top-level parameter name.
// The supported parameter set is closed; the catalog rejects topics
// that reference any parameter not in this switch (B-23). Keep the
// case list in lockstep with catalog.supportedSearchParameters.
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
	case "_lastUpdated":
		if meta, ok := body["meta"].(map[string]any); ok {
			if lu, ok := meta["lastUpdated"].(string); ok {
				return []any{lu}
			}
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

// equalsString is plain string equality. Mirrors submatcher.equalsString
// so the bare-clause path here matches the bare-clause path there
// (S-10.2). Falls through with false for non-string values.
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

// parseDateComparator splits "gt2026-01-01" → (cmp="gt", val=...).
func parseDateComparator(s string) (cmp, value string, ok bool) {
	for _, c := range []string{"eq", "ne", "gt", "lt", "ge", "le"} {
		if strings.HasPrefix(s, c) {
			rest := s[len(c):]
			if rest == "" || rest[0] < '0' || rest[0] > '9' {
				return "", "", false
			}
			return c, rest, true
		}
	}
	return "", "", false
}

func compareDate(have, cmp, want string) bool {
	hT, ok := parseFlexibleDate(have)
	if !ok {
		return false
	}
	wT, ok := parseFlexibleDate(want)
	if !ok {
		return false
	}
	switch cmp {
	case "eq":
		return hT.Equal(wT)
	case "ne":
		return !hT.Equal(wT)
	case "gt":
		return hT.After(wT)
	case "lt":
		return hT.Before(wT)
	case "ge":
		return hT.Equal(wT) || hT.After(wT)
	case "le":
		return hT.Equal(wT) || hT.Before(wT)
	}
	return false
}

func parseFlexibleDate(s string) (time.Time, bool) {
	t, _, ok := parseFlexibleDateWithFlag(s)
	return t, ok
}

// parseFlexibleDateWithFlag is parseFlexibleDate with an extra boolean
// reporting whether the imputed timezone was UTC (i.e., the input had
// no explicit tz). Callers that care can metric this so operators can
// tell when a topic's date comparator is silently coercing local time
// to UTC near boundaries (S-10.4).
func parseFlexibleDateWithFlag(s string) (time.Time, bool, bool) {
	// First try layouts WITH explicit timezone — RFC3339 already
	// includes ones like "2026-01-01T00:00:00Z" / "+05:00".
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, false, true
	}
	// Then layouts WITHOUT timezone — those parse-as-UTC silently.
	for _, layout := range []string{
		"2006-01-02T15:04:05",
		"2006-01-02",
		"2006-01",
		"2006",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true, true
		}
	}
	return time.Time{}, false, false
}

// foldICURoot applies ADR 0010 #4 case+accent folding using
// golang.org/x/text root locale.
//
// Order: NFD → strip combining marks → fold (lower) → NFC.
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

// runFHIRPath is a *minimal* gate, not a full evaluator. The LLD calls
// for a sandboxed FHIRPath with timeout, traversal limit, and deny-list
// (see docs/future-work.md P1.2).
//
// Until that evaluator lands, runFHIRPath recognizes only a closed
// set of expression shapes that the built-in topics need:
//
//   - "<Resource>.<field>.exists()" — true if the field has a value
//   - "<Resource>.status = '<v>'" — equality
//
// B-24: Every other expression returns *false* (fail-CLOSED). Earlier
// behavior fell through to `return true`, which silently fired every
// topic carrying an unrecognized FHIRPath like
// `Patient.deceased.empty()` on every change. Topics relying on
// shapes the matcher cannot evaluate are surfaced at load time
// (catalog.LoadStrictFHIRPath, when wired through) or via the
// SetUnknownFHIRPathReporter callback.
func runFHIRPath(expr string, resource, _ []byte) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true
	}
	var body map[string]any
	if err := json.Unmarshal(resource, &body); err != nil {
		reportMalformedResource("fhirpath", err)
		return false
	}
	if strings.HasSuffix(expr, ".exists()") {
		prefix := strings.TrimSuffix(expr, ".exists()")
		if i := strings.LastIndex(prefix, "."); i > 0 {
			field := prefix[i+1:]
			v, ok := body[field]
			if !ok {
				return false
			}
			switch x := v.(type) {
			case string:
				return x != ""
			case []any:
				return len(x) > 0
			default:
				_ = x
				return v != nil
			}
		}
	}
	if i := strings.Index(expr, ".status = '"); i > 0 {
		open := i + len(".status = '")
		end := strings.Index(expr[open:], "'")
		if end > 0 {
			want := expr[open : open+end]
			got, _ := body["status"].(string)
			return got == want
		}
	}
	// B-24: fail-CLOSED for unrecognized expressions. Notify the
	// optional reporter so wiring can bump a metric.
	if reporter := unknownFHIRPathReporter.Load(); reporter != nil {
		(*reporter)(expr)
	}
	return false
}

// IsRecognizedFHIRPath reports whether runFHIRPath knows how to
// evaluate expr without falling through to the fail-closed default.
// Catalog load uses it (in strict mode) to surface unsupported shapes.
func IsRecognizedFHIRPath(expr string) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true
	}
	if strings.HasSuffix(expr, ".exists()") {
		prefix := strings.TrimSuffix(expr, ".exists()")
		if i := strings.LastIndex(prefix, "."); i > 0 {
			return true
		}
	}
	if i := strings.Index(expr, ".status = '"); i > 0 {
		open := i + len(".status = '")
		if end := strings.Index(expr[open:], "'"); end > 0 {
			return true
		}
	}
	return false
}

// unknownFHIRPathReporter is an optional callback the wiring layer can
// install so it can bump a Prometheus counter for unrecognized FHIRPath
// expressions. Kept as a function pointer to keep this package free of
// a metrics-package dependency.
var unknownFHIRPathReporter atomic.Pointer[func(expr string)]

// SetUnknownFHIRPathReporter registers (or unsets, with nil) the
// callback invoked once per fail-closed FHIRPath evaluation. The
// matcher only ever calls it; the wiring layer owns the metric handle.
func SetUnknownFHIRPathReporter(fn func(expr string)) {
	if fn == nil {
		unknownFHIRPathReporter.Store(nil)
		return
	}
	unknownFHIRPathReporter.Store(&fn)
}

// unsupportedModifierReporter is the reporter for clauses whose
// modifier is recognized at catalog-load (e.g., :in) but the matcher
// has no implementation for. The :in modifier is the canonical case
// (S-10.3); ValueSet expansion is not wired through. The reporter
// fires once per fail-closed evaluation so wiring can bump a counter.
var unsupportedModifierReporter atomic.Pointer[func(modifier, parameter string)]

// SetUnsupportedModifierReporter installs (or unsets, with nil) the
// callback for unsupported-modifier hits. The matcher fails closed
// regardless; the reporter exists purely to emit a metric.
func SetUnsupportedModifierReporter(fn func(modifier, parameter string)) {
	if fn == nil {
		unsupportedModifierReporter.Store(nil)
		return
	}
	unsupportedModifierReporter.Store(&fn)
}

// reportUnsupportedModifier invokes the reporter (if any) with the
// modifier and parameter names. nil-safe.
func reportUnsupportedModifier(modifier, parameter string) {
	if r := unsupportedModifierReporter.Load(); r != nil {
		(*r)(modifier, parameter)
	}
}

// EvaluateClauseForTest is a thin testing seam over evaluateClause so
// the package-external test suite can drive the modifier-routing
// logic. Production callers route through Evaluate.
func EvaluateClauseForTest(c catalog.SearchClause, body map[string]any) bool {
	return evaluateClause(c, body)
}

// malformedResourceReporter fires when the matcher hits a JSON-decode
// failure on a resource body. The reporter exists so wiring can bump a
// counter while the matcher continues to fail closed (S-10.1).
var malformedResourceReporter atomic.Pointer[func(stage string, err error)]

// SetMalformedResourceReporter installs the reporter for malformed
// resource bodies. nil unsets.
func SetMalformedResourceReporter(fn func(stage string, err error)) {
	if fn == nil {
		malformedResourceReporter.Store(nil)
		return
	}
	malformedResourceReporter.Store(&fn)
}

func reportMalformedResource(stage string, err error) {
	if r := malformedResourceReporter.Load(); r != nil {
		(*r)(stage, err)
	}
}

// ---------- Worker / claim loop ----------

// CatalogProvider lets the worker re-read the snapshot per iteration
// (LLD: "Each worker takes a snapshot at the top of its claim-loop
// iteration").
//
// Contract: implementations MUST swap the returned *catalog.Catalog
// atomically. A function literal that returns the value of an ordinary
// variable IS NOT SAFE on its own — interface values are two words and
// a concurrent reload can produce a torn read where the data pointer
// belongs to the new catalog and the type pointer to the old one.
// Hot-reloading callers should use AtomicCatalogProvider (or guard the
// read with a sync.RWMutex) to satisfy this contract (B-29).
type CatalogProvider func() *catalog.Catalog

// AtomicCatalogProvider stores a *catalog.Catalog in an atomic.Pointer
// so a hot-reload writer and the matcher's claim-loop reader can swap
// + read concurrently without a mutex and without a torn-read race
// (B-29). Construct with NewAtomicCatalogProvider; pass AsProvider() to
// matcher.NewWorker.
type AtomicCatalogProvider struct {
	p atomic.Pointer[catalog.Catalog]
}

// NewAtomicCatalogProvider constructs an AtomicCatalogProvider with
// initial as the starting catalog. initial may be nil; Store(nil) is
// silently ignored thereafter so the worker always sees a non-nil
// catalog once one has been published.
func NewAtomicCatalogProvider(initial *catalog.Catalog) *AtomicCatalogProvider {
	a := &AtomicCatalogProvider{}
	if initial != nil {
		a.p.Store(initial)
	}
	return a
}

// Get returns the currently-loaded catalog. Safe to call concurrently
// with Store.
func (a *AtomicCatalogProvider) Get() *catalog.Catalog {
	if a == nil {
		return nil
	}
	return a.p.Load()
}

// Store swaps in a new catalog. Concurrent Store + Get is race-free.
// nil is silently ignored.
func (a *AtomicCatalogProvider) Store(c *catalog.Catalog) {
	if a == nil || c == nil {
		return
	}
	a.p.Store(c)
}

// AsProvider returns a CatalogProvider closure suitable for
// matcher.NewWorker. The closure shares state with the
// AtomicCatalogProvider — later Store calls are observed by the worker
// on its next tick.
func (a *AtomicCatalogProvider) AsProvider() CatalogProvider {
	return func() *catalog.Catalog { return a.Get() }
}

// Config is the matcher worker's tunables.
type Config struct {
	// PoolSize is the number of concurrent workers. Default 1.
	PoolSize int
	// ClaimBatchSize is the number of resource_changes rows claimed
	// per transaction. Default 1 (per LLD §"Configuration Knobs").
	ClaimBatchSize int32
	// IdlePollInterval is the delay between empty NoWork claims.
	IdlePollInterval time.Duration
	// DBBackoffInitial / DBBackoffMax bound transient DB-error
	// backoff.
	DBBackoffInitial time.Duration
	DBBackoffMax     time.Duration
	// MaxRowAttempts caps how many times a single resource_changes
	// row may fail the matcher transaction before it gets dead-lettered
	// (S-10.6). Default 8. A poison row that always panics or always
	// produces a tx error otherwise pins the worker forever.
	MaxRowAttempts int32
}

// ApplyDefaults fills zero values per the LLD §"Configuration Knobs".
func (c *Config) ApplyDefaults() {
	if c.PoolSize == 0 {
		c.PoolSize = 1
	}
	if c.ClaimBatchSize == 0 {
		c.ClaimBatchSize = 1
	}
	if c.IdlePollInterval == 0 {
		c.IdlePollInterval = 200 * time.Millisecond
	}
	if c.DBBackoffInitial == 0 {
		c.DBBackoffInitial = 100 * time.Millisecond
	}
	if c.DBBackoffMax == 0 {
		c.DBBackoffMax = 5 * time.Second
	}
	if c.MaxRowAttempts == 0 {
		c.MaxRowAttempts = 8
	}
}

// Worker is the claim-loop worker. It reads from resource_changes,
// evaluates against the catalog snapshot, writes ehr_events rows + the
// source-row mark-processed update inside one transaction (the
// transactional outbox).
type Worker struct {
	pool    *pgxpool.Pool
	catalog CatalogProvider
	rcsRepo *repos.ResourceChangesRepo
	ehrRepo *repos.EhrEventsRepo
	cfg     Config
}

// NewWorker constructs a Worker.
func NewWorker(pool *pgxpool.Pool, rcs *repos.ResourceChangesRepo, ehr *repos.EhrEventsRepo, cp CatalogProvider, cfg Config) *Worker {
	cfg.ApplyDefaults()
	return &Worker{pool: pool, rcsRepo: rcs, ehrRepo: ehr, catalog: cp, cfg: cfg}
}

// backoffReporter is the optional sampler the wiring layer can install
// so it can publish `matcher_backoff_seconds` to the metrics layer
// (N-1). Kept as a function pointer to avoid a metrics-package
// dependency in this hot path.
var backoffReporter atomic.Pointer[func(seconds float64)]

// SetBackoffReporter installs (or unsets, with nil) the matcher's
// per-worker backoff observer. The reporter is fired on every transient
// DB-error retry with the current backoff duration in seconds, so the
// host can wire a Prometheus gauge.
func SetBackoffReporter(fn func(seconds float64)) {
	if fn == nil {
		backoffReporter.Store(nil)
		return
	}
	backoffReporter.Store(&fn)
}

// Run blocks until ctx is canceled. Each iteration claims at most one
// row, evaluates, commits.
func (w *Worker) Run(ctx context.Context) error {
	if w == nil {
		return errors.New("matcher: nil worker")
	}
	backoff := w.cfg.DBBackoffInitial
	for {
		if ctx.Err() != nil {
			return nil
		}
		processed, err := w.tickOnce(ctx)
		if err != nil {
			if r := backoffReporter.Load(); r != nil {
				(*r)(backoff.Seconds())
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			backoff = nextBackoff(backoff, w.cfg.DBBackoffMax)
			continue
		}
		// Healthy tick — reset the gauge to 0 so the metric tracks
		// "current observed backoff" rather than "last observed
		// non-zero value." (N-1.)
		if r := backoffReporter.Load(); r != nil {
			(*r)(0)
		}
		backoff = w.cfg.DBBackoffInitial
		if !processed {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(w.cfg.IdlePollInterval):
			}
		}
	}
}

func nextBackoff(cur, ceiling time.Duration) time.Duration {
	next := cur * 2
	if next > ceiling {
		return ceiling
	}
	return next
}

// TickOnce performs one claim/process iteration and returns whether
// any row was processed. Exported so integration tests can drive the
// worker deterministically without a long-running goroutine.
func (w *Worker) TickOnce(ctx context.Context) (bool, error) {
	return w.tickOnce(ctx)
}

func (w *Worker) tickOnce(ctx context.Context) (bool, error) {
	cat := w.catalog()

	tx, err := w.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("matcher: begin tx: %w", err)
	}
	// txDone is set when the deferred rollback would be redundant —
	// either we explicitly committed, or we explicitly rolled back. The
	// previous name `committed` lied about the empty-claim early-exit
	// path which rolls back rather than commits (N-1).
	txDone := false
	defer func() {
		if !txDone {
			_ = tx.Rollback(ctx)
		}
	}()

	rows, err := w.rcsRepo.ClaimUnprocessed(ctx, tx, w.cfg.ClaimBatchSize)
	if err != nil {
		return false, fmt.Errorf("matcher: claim: %w", err)
	}
	if len(rows) == 0 {
		_ = tx.Rollback(ctx)
		txDone = true
		return false, nil
	}

	row := rows[0]
	ctx = correlation.WithID(ctx, row.CorrelationID.String())

	matches := Evaluate(cat, ResourceChange{
		ID:               row.ID,
		CreatedMonth:     row.CreatedMonth,
		ResourceType:     row.ResourceType,
		ChangeKind:       string(row.ChangeKind),
		Resource:         row.Resource,
		PreviousResource: row.PreviousResource,
		EventCode:        row.EventCode,
		CorrelationID:    row.CorrelationID,
		OccurredAt:       row.OccurredAt,
	})

	for i := range matches {
		m := &matches[i]
		_, _, err := w.ehrRepo.Insert(ctx, tx, repos.EhrEventRow{
			TopicURL:              m.TopicURL,
			Focus:                 m.Focus,
			ChangeKind:            repos.ChangeKind(m.ChangeKind),
			Resource:              m.Resource,
			PreviousResource:      m.PreviousResource,
			CorrelationID:         m.CorrelationID,
			OccurredAt:            m.OccurredAt,
			NotificationShapeHint: m.NotificationShapeHint,
			ResourceChangeID:      m.ResourceChangeID,
		})
		if err != nil {
			return false, fmt.Errorf("matcher: insert ehr_events: %w", err)
		}
	}

	if _, err := w.rcsRepo.MarkProcessed(ctx, tx, row.ID, row.CreatedMonth); err != nil {
		return false, fmt.Errorf("matcher: mark processed: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("matcher: commit: %w", err)
	}
	txDone = true
	return true, nil
}
