// Copyright the fhir-subscriptions-foss authors.
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
package matcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/cases"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/observability/correlation"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/storage/repos"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/topics/catalog"
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
func evaluateSearchExpression(expr *catalog.SearchExpression, resource []byte) bool {
	if expr == nil {
		return true
	}
	var m map[string]any
	if err := json.Unmarshal(resource, &m); err != nil {
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
		// pass.
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
		}
		return false
	}
	return false
}

// extractFieldValues yields the values for a top-level parameter name.
// We support the common search parameters built-in topics and the
// initial conformance set need; unknown parameters return no values
// (the clause fails closed).
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
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02",
		"2006-01",
		"2006",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
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
// for a sandboxed FHIRPath with timeout, traversal limit, and deny-list.
// Until the dedicated evaluator lands, we recognize a small set of
// expressions that the built-in topics need:
//
//   - "<Resource>.<field>.exists()" — true if the field has a value
//   - "<Resource>.status = '<v>'" — equality
//   - other expressions: pass-through (return true). Operator metric
//     should flag unknown FHIRPath usage; the catalog preserves the
//     source so the future evaluator can replay deterministically.
func runFHIRPath(expr string, resource, _ []byte) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true
	}
	var body map[string]any
	if err := json.Unmarshal(resource, &body); err != nil {
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
	return true
}

// ---------- Worker / claim loop ----------

// CatalogProvider lets the worker re-read the snapshot per iteration
// (LLD: "Each worker takes a snapshot at the top of its claim-loop
// iteration").
type CatalogProvider func() *catalog.Catalog

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
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			backoff = nextBackoff(backoff, w.cfg.DBBackoffMax)
			continue
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
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	rows, err := w.rcsRepo.ClaimUnprocessed(ctx, tx, w.cfg.ClaimBatchSize)
	if err != nil {
		return false, fmt.Errorf("matcher: claim: %w", err)
	}
	if len(rows) == 0 {
		_ = tx.Rollback(ctx)
		committed = true
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
	committed = true
	return true, nil
}
