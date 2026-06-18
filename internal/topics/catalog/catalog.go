// Copyright the fhir-subscriptions-foss authors.
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
// Hot-reload, persistence to subscription_topics, and operator-mounted
// directories are handled by callers (lifecycle / topics module).
package catalog

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

//go:embed schemas/subscription_topic.schema.json
var schemaFS embed.FS

// SchemaURI is the canonical $id of the embedded schema.
const SchemaURI = "https://fhir-subscriptions-foss.org/schemas/subscription_topic.schema.json"

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
	CanonicalURL      string
	Version           string
	Title             string
	Status            string
	Source            Source
	Origin            string
	Triggers          []Trigger
	EventCodes        []string
	FilterBy          []FilterParameter
	NotificationShape NotificationShape
	// RawJSON is the original body — kept for serving GET
	// /SubscriptionTopic exactly as authored.
	RawJSON []byte
}

// Catalog is the immutable handle the matcher reads.
type Catalog struct {
	topics      []*Topic
	byURL       map[string]*Topic
	byResource  map[string][]*Topic
	byEventCode map[string][]*Topic
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

// Rejection records why a single raw topic failed to enter the catalog.
type Rejection struct {
	Origin string
	URL    string
	Reason string
}

// Report is what Load returns.
type Report struct {
	Catalog  *Catalog
	Rejected []Rejection
}

// supportedTokenModifiers, supportedReferenceModifiers, etc., enforce
// ADR 0006 (no CQL, no regex; only the documented subset).
var (
	supportedTokenModifiers     = stringSet("not", "missing", "in")
	supportedReferenceModifiers = stringSet("identifier", "missing")
	supportedStringModifiers    = stringSet("contains", "missing")
	supportedDateComparators    = stringSet("eq", "ne", "gt", "lt", "ge", "le")
	// Unsupported modifiers we explicitly call out so the rejection
	// message names them.
	knownUnsupportedModifiers = stringSet("above", "below", "type", "text")
)

func stringSet(ss ...string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}

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
	// Order of concatenation matters only for stable iteration in
	// tests; the priority comparison below is what actually decides
	// who wins.
	all := make([]entry, 0, len(sources.BuiltIn)+len(sources.Adapter)+len(sources.Operator))
	for _, r := range sources.BuiltIn {
		all = append(all, entry{r, SourceBuiltIn})
	}
	for _, r := range sources.Adapter {
		all = append(all, entry{r, SourceAdapter})
	}
	for _, r := range sources.Operator {
		all = append(all, entry{r, SourceOperator})
	}

	type winner struct {
		topic *Topic
		src   Source
	}
	winners := map[string]winner{} // key: url + "|" + version
	rejected := []Rejection{}

	for _, e := range all {
		topic, rejection := compileOne(sch, e.raw, e.source)
		if rejection != nil {
			rejected = append(rejected, *rejection)
			continue
		}
		key := topic.CanonicalURL + "|" + topic.Version
		prev, ok := winners[key]
		if !ok || sourcePriority(e.source) > sourcePriority(prev.src) {
			winners[key] = winner{topic, e.source}
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

	return Report{Catalog: cat, Rejected: rejected}, nil
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
	eventCodes := []string{}
	for _, ev := range parsed.EventTrigger {
		for _, c := range ev.Event.Coding {
			if c.Code != "" {
				eventCodes = append(eventCodes, c.Code)
			}
		}
	}

	// 5. canFilterBy whitelist.
	filters := make([]FilterParameter, 0, len(parsed.CanFilterBy))
	for _, cf := range parsed.CanFilterBy {
		if cf.FilterParameter == "" {
			continue
		}
		filters = append(filters, FilterParameter{
			Resource:  cf.Resource,
			Parameter: cf.FilterParameter,
		})
	}

	// 6. notificationShape (one-or-more entries; we collapse to one
	// shape; downstream consumers care about the list of includes).
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
// modifiers / comparators outside the supported subset.
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
	Description          string           `json:"description"`
	Resource             string           `json:"resource"`
	SupportedInteraction []string         `json:"supportedInteraction"`
	QueryCriteria        *rawQueryCrit    `json:"queryCriteria"`
	FhirPathCriteria     string           `json:"fhirPathCriteria"`
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
