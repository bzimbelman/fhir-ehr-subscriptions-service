// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package catalog is the SubscriptionTopic catalog: load, validate
// against the embedded JSON Schema, compile each topic into the
// in-memory form the matcher consumes.
//
// This package owns:
//   - The Sources gather model (built-in / adapter / operator with
//     priority Operator > Adapter > BuiltIn)
//   - JSON Schema validation of every topic before compile
//   - Compile-time rejection of search-parameter features outside
//     the supported subset documented in ADR 0006
//   - The immutable Catalog handle the matcher reads
//
// The supported FHIR search-parameter set is closed: every parameter
// referenced by a topic's queryCriteria or canFilterBy MUST be in
// SupportedSearchParameters() or the topic is rejected at load time
// (B-23). Keep that list in lockstep with the matcher's
// extractFieldValues switch.
//
// Hot-reload, persistence to subscription_topics, and operator-mounted
// directories are handled by callers (lifecycle / topics module).
package catalog

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

//go:embed schemas/subscription_topic.schema.json
var schemaFS embed.FS

// SchemaURI is the canonical $id of the embedded schema.
const SchemaURI = "https://fhir-ehr-subscriptions-service.org/schemas/subscription_topic.schema.json"

// schemaOnce builds the compiled JSON Schema lazily.
var (
	schemaOnce sync.Once
	schema     *jsonschema.Schema
	schemaErr  error
)

func compiledSchema() (*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		raw, err := schemaFS.ReadFile("schemas/subscription_topic.schema.json")
		if err != nil {
			schemaErr = fmt.Errorf("catalog: read embedded schema: %w", err)
			return
		}
		c := jsonschema.NewCompiler()
		if err := c.AddResource(SchemaURI, bytes.NewReader(raw)); err != nil {
			schemaErr = fmt.Errorf("catalog: add schema resource: %w", err)
			return
		}
		schema, schemaErr = c.Compile(SchemaURI)
	})
	return schema, schemaErr
}

// Source labels the origin precedence of a topic.
type Source string

// Source values. Higher precedence wins on (url, version) collision.
const (
	SourceBuiltIn  Source = "builtin"
	SourceAdapter  Source = "adapter"
	SourceOperator Source = "operator"
)

// RawTopic is one undecoded SubscriptionTopic resource bundled with the
// origin label used in error messages and metrics.
type RawTopic struct {
	// Origin is a human-readable identifier (file path, adapter id,
	// "builtin/<slug>", etc.) used in rejection messages.
	Origin string
	// Bytes is the raw JSON body of the SubscriptionTopic resource.
	Bytes []byte
}

// Sources groups raw topics by their precedence layer.
type Sources struct {
	BuiltIn  []RawTopic
	Adapter  []RawTopic
	Operator []RawTopic
}

// Trigger is the compiled form of one SubscriptionTopic.resourceTrigger.
type Trigger struct {
	// ResourceTypes is a set: triggers may scope to one or more
	// FHIR resource types. The matcher's Step 1 gate consults this.
	ResourceTypes map[string]bool
	// Interactions is a set of {"create","update","delete"}.
	Interactions map[string]bool
	// PreviousCriteria is the queryCriteria.previous expression in
	// compiled form (or nil if absent).
	PreviousCriteria *SearchExpression
	// CurrentCriteria is the queryCriteria.current expression.
	CurrentCriteria *SearchExpression
	// RequireBoth is queryCriteria.requireBoth.
	RequireBoth bool
	// FHIRPath is the source text of fhirPathCriteria. The matcher
	// passes it to the FHIRPath sandbox at run time.
	FHIRPath string
}

// SearchExpression is the parsed form of a FHIR search-parameter
// expression as used in queryCriteria.previous / .current. Each clause
// is one parameter=value pair joined to the others by AND.
type SearchExpression struct {
	// Source is the original string; preserved for diagnostics.
	Source string
	// Clauses is the AND-joined parameter list.
	Clauses []SearchClause
}

// SearchClause is one (parameter, modifier, comparator, value) tuple.
type SearchClause struct {
	Parameter  string
	Modifier   string // "" | not | identifier | contains | missing | in
	Comparator string // "" | eq | ne | gt | lt | ge | le
	Value      string
}

// FilterParameter is one entry in canFilterBy.
type FilterParameter struct {
	Resource  string
	Parameter string
}

// NotificationShape carries the topic's _include / _revinclude hints.
type NotificationShape struct {
	Resource    string
	Includes    []string
	RevIncludes []string
}

// Topic is the compiled, immutable in-memory form of a
// SubscriptionTopic. Matcher reads only this — never the raw JSON.
type Topic struct {
	CanonicalURL string
	Version      string
	Title        string
	Status       string
	Source       Source
	Origin       string
	Triggers     []Trigger
	EventCodes   []string // legacy: code-only; preserved for callers
	// EventCodings carries the (system, code) pairs from
	// eventTrigger so consumers can disambiguate cross-system code
	// collisions. EventCodes (above) keeps the code-only view for
	// backwards compatibility (S-11.2).
	EventCodings      []EventCoding
	FilterBy          []FilterParameter
	NotificationShape NotificationShape
	// RawJSON is the original body — kept for serving GET
	// /SubscriptionTopic exactly as authored.
	RawJSON []byte
}

// EventCoding is one (system, code) pair from an eventTrigger entry.
type EventCoding struct {
	System string
	Code   string
}

// Catalog is the immutable handle the matcher reads.
//
// # Immutability contract (N-1)
//
// A Catalog returned by Load is treated as read-only by every consumer
// in this codebase. Mutating a *Catalog or any *Topic it owns after it
// has been published (e.g., via AtomicCatalogProvider.Store) is
// undefined and will race with concurrent matcher reads. To roll out a
// new catalog, build a new *Catalog via Load and atomically swap it
// in; never mutate in place.
//
// # RawJSON lifetime
//
// Topic.RawJSON is materialized at Load time so subsequent
// /SubscriptionTopic GETs do not have to re-canonicalize. For very
// large catalogs (10k+ topics) this is a meaningful in-memory cost;
// the RawJSON bytes are never mutated, so a future on-demand-load
// variant is a backwards-compatible refactor (load returns the bytes
// from a sidecar file/blob the first time RawJSON() is called).
// Today the cost is intentional — the API never serializes one row
// per request.
type Catalog struct {
	topics      []*Topic
	byURL       map[string]*Topic
	byResource  map[string][]*Topic
	byEventCode map[string][]*Topic
	rejected    []Rejection
	overridden  []Override
}

// All returns every topic. Order is canonical-URL ascending.
func (c *Catalog) All() []*Topic {
	if c == nil {
		return nil
	}
	out := make([]*Topic, len(c.topics))
	copy(out, c.topics)
	return out
}

// ByResourceType returns topics with at least one resourceTrigger
// scoped to the given FHIR resource type.
func (c *Catalog) ByResourceType(resourceType string) []*Topic {
	if c == nil {
		return nil
	}
	return append([]*Topic(nil), c.byResource[resourceType]...)
}

// ByEventCode returns topics with an eventTrigger matching the given
// vendor event code (direct equality, no expression evaluation).
func (c *Catalog) ByEventCode(code string) []*Topic {
	if c == nil {
		return nil
	}
	return append([]*Topic(nil), c.byEventCode[code]...)
}

// Get returns the topic with the given canonical URL or nil.
func (c *Catalog) Get(canonicalURL string) *Topic {
	if c == nil {
		return nil
	}
	return c.byURL[canonicalURL]
}

// Rejected returns the per-load rejection diagnostics surfaced through
// the catalog so /readyz / healthcheck endpoints can read them without
// keeping the original Report (B-25).
func (c *Catalog) Rejected() []Rejection {
	if c == nil {
		return nil
	}
	out := make([]Rejection, len(c.rejected))
	copy(out, c.rejected)
	return out
}

// Overridden returns the (url, version) pairs where a higher-priority
// override was rejected at load time and the catalog fell back to a
// working lower-priority topic (B-25).
func (c *Catalog) Overridden() []Override {
	if c == nil {
		return nil
	}
	out := make([]Override, len(c.overridden))
	copy(out, c.overridden)
	return out
}

// Rejection records why a single raw topic failed to enter the catalog.
type Rejection struct {
	Origin string
	URL    string
	Reason string
}

// Override records a (url, version) pair where a higher-priority
// candidate failed to compile and the catalog fell back to a working
// lower-priority topic. Surfaced so an operator typo cannot silently
// shadow a working built-in (B-25).
type Override struct {
	URL        string
	Version    string
	FromOrigin string // origin of the topic actually used (lower priority)
	FromSource Source
	ToOrigin   string // origin of the rejected higher-priority candidate
	ToSource   Source
	Reason     string
}

// LogFields returns a structured map suitable for slog/logr emitters.
// Wiring layers iterate Catalog.Overridden() at startup and emit one
// record per Override so operators see exactly which higher-priority
// candidate was shadowed by a lower-priority working topic — a typo in
// an operator-supplied catalog should never silently shadow a built-in
// without an audit trail (N-1).
func (o Override) LogFields() map[string]any {
	return map[string]any{
		"event":           "topic_override_fallback",
		"url":             o.URL,
		"version":         o.Version,
		"used_origin":     o.FromOrigin,
		"used_source":     string(o.FromSource),
		"shadowed_origin": o.ToOrigin,
		"shadowed_source": string(o.ToSource),
		"reason":          o.Reason,
	}
}

// Report is what Load returns.
type Report struct {
	Catalog    *Catalog
	Rejected   []Rejection
	Overridden []Override
}

// supportedTokenModifiers, supportedReferenceModifiers, etc., enforce
// ADR 0006 (no CQL, no regex; only the documented subset).
//
// P1.3: :in is *recognized* by the parser but the matcher fails closed
// at evaluation because no ValueSet expander ships with v1. Per LLD
// the right behavior is fail-loud at catalog load: operators must know
// their topic cannot fire before deploying it. allowInModifier flips
// the rejection at load time only when the host has wired a ValueSet
// expander (not yet exposed). Until then, :in topics are rejected at
// load with a clear error pointing at this future-work item.
var (
	supportedTokenModifiers     = stringSet("not", "missing", "in")
	supportedReferenceModifiers = stringSet("identifier", "missing")
	supportedStringModifiers    = stringSet("contains", "missing")
	// Unsupported modifiers we explicitly call out so the rejection
	// message names them. Compile-time list per ADR 0006.
	knownUnsupportedModifiers = stringSet("above", "below", "type", "text")

	// supportedSearchParameters is the closed set of FHIR search
	// parameters the matcher's extractFieldValues can resolve. A
	// topic that references any other parameter is rejected at
	// catalog load time (B-23) so it cannot silently fail to match
	// at run time.
	//
	// Keep in lockstep with internal/matcher.matcher.go's
	// extractFieldValues switch.
	supportedSearchParameters = stringSet(
		"status",
		"subject",
		"patient",
		"code",
		"category",
		"name",
		"_lastUpdated",
	)
)

// SupportedSearchParameters returns the closed list of FHIR search
// parameters the matcher can evaluate. Topics referencing any other
// parameter are rejected at catalog load.
func SupportedSearchParameters() []string {
	out := make([]string, 0, len(supportedSearchParameters))
	for k := range supportedSearchParameters {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// IsSupportedSearchParameter reports whether p is in the supported
// FHIR search-parameter subset.
func IsSupportedSearchParameter(p string) bool {
	return supportedSearchParameters[p]
}

func stringSet(ss ...string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}

// ErrStrictTopicLoad is returned by LoadStrict when at least one topic
// failed to compile during a strict-mode load. The error wraps every
// rejection so operators see all failures at once.
var ErrStrictTopicLoad = errors.New("catalog: strict load: one or more topics rejected")

// Load gathers topics from every source, validates each against the
// embedded JSON Schema, compiles to Topic, applies precedence, and
// returns the immutable Catalog plus per-topic rejections.
//
// Load never returns a fatal error for content errors — those flow
// through Rejected. err is non-nil only for misconfiguration of the
// loader itself (e.g., the embedded schema fails to compile).
func Load(sources Sources) (Report, error) {
	sch, err := compiledSchema()
	if err != nil {
		return Report{}, err
	}

	type entry struct {
		raw    RawTopic
		source Source
	}
	// Walk sources in priority order: highest priority first. The
	// first successfully-compiled candidate per (url, version) wins;
	// later (lower-priority) compile-OK candidates are ignored. A
	// later success after a higher-priority *failure* is recorded as
	// an Override fallback so operators see they shadowed a typo.
	all := make([]entry, 0, len(sources.BuiltIn)+len(sources.Adapter)+len(sources.Operator))
	for _, r := range sources.Operator {
		all = append(all, entry{r, SourceOperator})
	}
	for _, r := range sources.Adapter {
		all = append(all, entry{r, SourceAdapter})
	}
	for _, r := range sources.BuiltIn {
		all = append(all, entry{r, SourceBuiltIn})
	}

	type winner struct {
		topic *Topic
		src   Source
	}
	winners := map[string]winner{} // key: url + "|" + version
	rejected := []Rejection{}
	overridden := []Override{}
	// failedHigher tracks the first higher-priority failure per
	// (url, version) so we can record the Override when a lower
	// priority topic later succeeds.
	type failure struct {
		origin string
		src    Source
		reason string
	}
	failedHigher := map[string]failure{}

	tryParseURLVersion := func(b []byte) (string, string) {
		var p struct {
			URL     string `json:"url"`
			Version string `json:"version"`
		}
		_ = json.Unmarshal(b, &p)
		return p.URL, p.Version
	}

	for _, e := range all {
		topic, rejection := compileOne(sch, e.raw, e.source)
		if rejection != nil {
			rejected = append(rejected, *rejection)
			// Record as a higher-priority failure keyed on
			// (url, version) when those are knowable. URL/Version
			// may be empty for a parse failure; those failures
			// can never be overridden anyway.
			if rejection.URL != "" {
				url, version := tryParseURLVersion(e.raw.Bytes)
				if url == "" {
					url = rejection.URL
				}
				if version == "" {
					// best-effort
					version = "*"
				}
				key := url + "|" + version
				if _, has := failedHigher[key]; !has {
					failedHigher[key] = failure{
						origin: e.raw.Origin,
						src:    e.source,
						reason: rejection.Reason,
					}
				}
			} else {
				url, version := tryParseURLVersion(e.raw.Bytes)
				if url != "" && version != "" {
					key := url + "|" + version
					if _, has := failedHigher[key]; !has {
						failedHigher[key] = failure{
							origin: e.raw.Origin,
							src:    e.source,
							reason: rejection.Reason,
						}
					}
				}
			}
			continue
		}
		key := topic.CanonicalURL + "|" + topic.Version
		if _, ok := winners[key]; ok {
			// A higher-priority winner is already in the map.
			// Skip silently.
			continue
		}
		winners[key] = winner{topic, e.source}

		// If a higher-priority candidate had failed at this
		// (url, version), record the override fallback.
		if f, ok := failedHigher[key]; ok && sourcePriority(f.src) > sourcePriority(e.source) {
			overridden = append(overridden, Override{
				URL:        topic.CanonicalURL,
				Version:    topic.Version,
				FromOrigin: e.raw.Origin,
				FromSource: e.source,
				ToOrigin:   f.origin,
				ToSource:   f.src,
				Reason:     f.reason,
			})
		}
	}

	all2 := make([]*Topic, 0, len(winners))
	for _, w := range winners {
		all2 = append(all2, w.topic)
	}
	sort.Slice(all2, func(i, j int) bool {
		if all2[i].CanonicalURL != all2[j].CanonicalURL {
			return all2[i].CanonicalURL < all2[j].CanonicalURL
		}
		return all2[i].Version < all2[j].Version
	})

	cat := &Catalog{
		topics:      all2,
		byURL:       make(map[string]*Topic, len(all2)),
		byResource:  make(map[string][]*Topic),
		byEventCode: make(map[string][]*Topic),
		rejected:    append([]Rejection(nil), rejected...),
		overridden:  append([]Override(nil), overridden...),
	}
	for _, t := range all2 {
		cat.byURL[t.CanonicalURL] = t
		seenRT := map[string]bool{}
		for _, tr := range t.Triggers {
			for rt := range tr.ResourceTypes {
				if seenRT[rt] {
					continue
				}
				seenRT[rt] = true
				cat.byResource[rt] = append(cat.byResource[rt], t)
			}
		}
		for _, code := range t.EventCodes {
			cat.byEventCode[code] = append(cat.byEventCode[code], t)
		}
	}

	return Report{Catalog: cat, Rejected: rejected, Overridden: overridden}, nil
}

// LoadStrict is Load with one extra contract: any non-empty Rejected
// list causes an error. Use this for `--strict-topics` startup mode
// (B-25) where the operator wants the process to refuse to start with
// a broken catalog rather than silently lose topics.
func LoadStrict(sources Sources) (Report, error) {
	rep, err := Load(sources)
	if err != nil {
		return rep, err
	}
	if len(rep.Rejected) > 0 {
		var b strings.Builder
		b.WriteString(ErrStrictTopicLoad.Error())
		for _, r := range rep.Rejected {
			b.WriteString("\n  - origin=")
			b.WriteString(r.Origin)
			if r.URL != "" {
				b.WriteString(" url=")
				b.WriteString(r.URL)
			}
			b.WriteString(" reason=")
			b.WriteString(r.Reason)
		}
		return rep, fmt.Errorf("%s", b.String())
	}
	return rep, nil
}

func sourcePriority(s Source) int {
	switch s {
	case SourceOperator:
		return 3
	case SourceAdapter:
		return 2
	case SourceBuiltIn:
		return 1
	}
	return 0
}

// compileOne validates and compiles a single raw topic. Returns either
// (topic, nil) on success or (nil, *Rejection) on failure.
func compileOne(sch *jsonschema.Schema, raw RawTopic, src Source) (*Topic, *Rejection) {
	// 1. Schema validate.
	var doc any
	if err := json.Unmarshal(raw.Bytes, &doc); err != nil {
		return nil, &Rejection{Origin: raw.Origin, Reason: "json parse: " + err.Error()}
	}
	if err := sch.Validate(doc); err != nil {
		return nil, &Rejection{Origin: raw.Origin, Reason: schemaError(err)}
	}

	// 2. Decode into typed view.
	var parsed rawSubscriptionTopic
	if err := json.Unmarshal(raw.Bytes, &parsed); err != nil {
		return nil, &Rejection{Origin: raw.Origin, Reason: "decode: " + err.Error()}
	}

	// Defensive double-check (schema enforces these but we surface a
	// readable error if the schema is ever loosened).
	if parsed.URL == "" {
		return nil, &Rejection{Origin: raw.Origin, Reason: "url is required"}
	}
	if parsed.Version == "" {
		return nil, &Rejection{Origin: raw.Origin, URL: parsed.URL, Reason: "version is required"}
	}

	// 3. Compile triggers.
	triggers := make([]Trigger, 0, len(parsed.ResourceTrigger))
	for i, rt := range parsed.ResourceTrigger {
		tr, err := compileTrigger(rt)
		if err != nil {
			return nil, &Rejection{
				Origin: raw.Origin,
				URL:    parsed.URL,
				Reason: fmt.Sprintf("resourceTrigger[%d]: %s", i, err.Error()),
			}
		}
		triggers = append(triggers, tr)
	}

	// 4. Event-trigger codes (no expression evaluation per ADR 0006).
	// S-11.2: also retain the (system, code) pairs so callers that
	// care about cross-system disambiguation are not forced to
	// re-parse the raw JSON.
	eventCodes := []string{}
	eventCodings := []EventCoding{}
	for _, ev := range parsed.EventTrigger {
		for _, c := range ev.Event.Coding {
			if c.Code != "" {
				eventCodes = append(eventCodes, c.Code)
				eventCodings = append(eventCodings, EventCoding{System: c.System, Code: c.Code})
			}
		}
	}

	// 5. canFilterBy whitelist.
	filters := make([]FilterParameter, 0, len(parsed.CanFilterBy))
	for _, cf := range parsed.CanFilterBy {
		if cf.FilterParameter == "" {
			continue
		}
		if !IsSupportedSearchParameter(cf.FilterParameter) {
			return nil, &Rejection{
				Origin: raw.Origin,
				URL:    parsed.URL,
				Reason: fmt.Sprintf("canFilterBy filterParameter %q is unsupported (matcher cannot evaluate it); supported parameters: %v", cf.FilterParameter, SupportedSearchParameters()),
			}
		}
		filters = append(filters, FilterParameter{
			Resource:  cf.Resource,
			Parameter: cf.FilterParameter,
		})
	}

	// 6. notificationShape. The FHIR spec allows an array so a topic
	// can declare per-resource shape selection (one entry per
	// resource type). v1's builder honors a single shape only — it
	// has no per-resource shape selection at delivery prep time. A
	// silent collapse (last-write-wins on Resource, concatenated
	// includes) hides the divergence from operators and produces
	// incorrect Bundles. Reject at load (S-11.3) so the failure is
	// visible during deploy. Per-entry compile end-to-end is tracked
	// as future work.
	if len(parsed.NotificationShape) > 1 {
		return nil, &Rejection{
			Origin: raw.Origin,
			URL:    parsed.URL,
			Reason: fmt.Sprintf("multi-entry notificationShape is not supported (topic %q declares %d entries; v1 builder honors a single shape only — split into separate topics or wait for per-entry compile)", parsed.URL, len(parsed.NotificationShape)),
		}
	}
	shape := NotificationShape{}
	for _, ns := range parsed.NotificationShape {
		shape.Resource = ns.Resource
		shape.Includes = append(shape.Includes, ns.Include...)
		shape.RevIncludes = append(shape.RevIncludes, ns.RevInclude...)
	}

	t := &Topic{
		CanonicalURL:      parsed.URL,
		Version:           parsed.Version,
		Title:             parsed.Title,
		Status:            parsed.Status,
		Source:            src,
		Origin:            raw.Origin,
		Triggers:          triggers,
		EventCodes:        eventCodes,
		EventCodings:      eventCodings,
		FilterBy:          filters,
		NotificationShape: shape,
		RawJSON:           append([]byte(nil), raw.Bytes...),
	}
	return t, nil
}

func compileTrigger(rt rawResourceTrigger) (Trigger, error) {
	tr := Trigger{
		ResourceTypes: map[string]bool{},
		Interactions:  map[string]bool{},
	}
	if rt.Resource == "" {
		return tr, fmt.Errorf("resource is required")
	}
	tr.ResourceTypes[rt.Resource] = true
	for _, ix := range rt.SupportedInteraction {
		// S-11.1: defense-in-depth — schema already enforces the
		// enum, but if the schema is ever loosened we still reject
		// here so a typo cannot silently never-match.
		if ix != "create" && ix != "update" && ix != "delete" {
			return tr, fmt.Errorf("supportedInteraction %q is not one of create|update|delete", ix)
		}
		tr.Interactions[ix] = true
	}
	if rt.QueryCriteria != nil {
		if rt.QueryCriteria.Previous != "" {
			ex, err := parseSearchExpression(rt.QueryCriteria.Previous)
			if err != nil {
				return tr, fmt.Errorf("queryCriteria.previous: %w", err)
			}
			tr.PreviousCriteria = ex
		}
		if rt.QueryCriteria.Current != "" {
			ex, err := parseSearchExpression(rt.QueryCriteria.Current)
			if err != nil {
				return tr, fmt.Errorf("queryCriteria.current: %w", err)
			}
			tr.CurrentCriteria = ex
		}
		tr.RequireBoth = rt.QueryCriteria.RequireBoth
	}
	tr.FHIRPath = rt.FhirPathCriteria
	return tr, nil
}

// parseSearchExpression splits "a=b&c:not=d" into clauses and rejects
// modifiers / comparators / parameters outside the supported subset.
func parseSearchExpression(s string) (*SearchExpression, error) {
	exp := &SearchExpression{Source: s}
	for _, raw := range splitAmp(s) {
		if raw == "" {
			continue
		}
		eq := strings.IndexByte(raw, '=')
		if eq < 0 {
			return nil, fmt.Errorf("clause %q: missing '='", raw)
		}
		left, value := raw[:eq], raw[eq+1:]
		clause, err := parseLeft(left)
		if err != nil {
			return nil, fmt.Errorf("clause %q: %w", raw, err)
		}
		clause.Value = value
		// B-23: every parameter must be evaluable by the matcher.
		// If it is not in the supported subset, fail at load time
		// rather than at run time (where it would silently fail
		// closed and the topic would never fire).
		if !IsSupportedSearchParameter(clause.Parameter) {
			return nil, fmt.Errorf("clause %q: unsupported search parameter %q (matcher cannot evaluate it); supported parameters: %v",
				raw, clause.Parameter, SupportedSearchParameters())
		}
		exp.Clauses = append(exp.Clauses, clause)
	}
	return exp, nil
}

func parseLeft(left string) (SearchClause, error) {
	c := SearchClause{}
	// modifier marker is ':'. comparator markers are not applied
	// here — they're embedded in the value (eq / ne / gt / lt / ge /
	// le for date params, e.g. "_lastUpdated=gt2026-01-01").
	if i := strings.IndexByte(left, ':'); i >= 0 {
		c.Parameter = left[:i]
		c.Modifier = left[i+1:]
	} else {
		c.Parameter = left
	}
	if c.Parameter == "" {
		return c, fmt.Errorf("missing parameter name")
	}
	if c.Modifier != "" {
		if knownUnsupportedModifiers[c.Modifier] {
			return c, fmt.Errorf("unsupported modifier %q (not in subset per ADR 0006)", c.Modifier)
		}
		if !supportedTokenModifiers[c.Modifier] &&
			!supportedReferenceModifiers[c.Modifier] &&
			!supportedStringModifiers[c.Modifier] {
			return c, fmt.Errorf("unsupported modifier %q", c.Modifier)
		}
		// P1.3: :in requires a ValueSet expander; v1 ships without one.
		// Until the expander lands (future-work P1.3), reject the topic
		// at load so operators see the failure during deploy rather than
		// silently miss every notification at runtime.
		if c.Modifier == "in" {
			return c, fmt.Errorf("modifier :in requires a ValueSet expander not yet wired (future-work P1.3); topic must use a different filter shape until ValueSet expansion ships")
		}
	}
	return c, nil
}

func splitAmp(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "&")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

// schemaError summarizes a jsonschema validation error into a single
// short reason string suitable for an operator log line.
func schemaError(err error) string {
	if err == nil {
		return ""
	}
	// jsonschema/v5 errors include all leaf failures; we surface a
	// compact view.
	msg := err.Error()
	// Heuristic: callers expect "url" / "version" appearing in the
	// reason when those fields are missing. The library already
	// includes them in the human form.
	if strings.Contains(msg, "missing properties") {
		return msg
	}
	return msg
}

// rawSubscriptionTopic is the JSON-decoder mirror.
type rawSubscriptionTopic struct {
	URL               string                 `json:"url"`
	Version           string                 `json:"version"`
	Title             string                 `json:"title"`
	Description       string                 `json:"description"`
	Status            string                 `json:"status"`
	ResourceTrigger   []rawResourceTrigger   `json:"resourceTrigger"`
	EventTrigger      []rawEventTrigger      `json:"eventTrigger"`
	CanFilterBy       []rawCanFilterBy       `json:"canFilterBy"`
	NotificationShape []rawNotificationShape `json:"notificationShape"`
}

type rawResourceTrigger struct {
	Description          string        `json:"description"`
	Resource             string        `json:"resource"`
	SupportedInteraction []string      `json:"supportedInteraction"`
	QueryCriteria        *rawQueryCrit `json:"queryCriteria"`
	FhirPathCriteria     string        `json:"fhirPathCriteria"`
}

type rawQueryCrit struct {
	Previous    string `json:"previous"`
	Current     string `json:"current"`
	RequireBoth bool   `json:"requireBoth"`
}

type rawEventTrigger struct {
	Event struct {
		Coding []struct {
			System string `json:"system"`
			Code   string `json:"code"`
		} `json:"coding"`
	} `json:"event"`
	Resource string `json:"resource"`
}

type rawCanFilterBy struct {
	Resource        string `json:"resource"`
	FilterParameter string `json:"filterParameter"`
}

type rawNotificationShape struct {
	Resource   string   `json:"resource"`
	Include    []string `json:"include"`
	RevInclude []string `json:"revInclude"`
}
