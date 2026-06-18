// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package hydration is the in-process FHIR `_include` / `_revinclude`
// expander the Stage-4 builder will call to fold related resources into
// a notification Bundle. It is a thin compositor over the adapter SPI's
// HydrationService: it walks each match resource, extracts the
// references the include rules call out, asks the HydrationService to
// fetch them, and returns the matched + included resource set with
// FHIR `searchset` semantics (`searchEntry.mode = include`).
//
// MVP scope (story #64):
//   - `_include` only via the default JSON ReferenceExtractor (covers
//     Reference shapes used in v1 wire shape: bare `{"reference":"T/i"}`
//     scalars and `[]{"reference":"T/i"}` arrays).
//   - `_revinclude` is supported only when the caller plugs in a
//     custom ReferenceExtractor that knows how to compute the inverse
//     refs (in production this would be a search query against the EHR;
//     the adapter SPI only exposes point Fetch).
//   - Recursive includes are bounded by MaxDepth (default 3) and total
//     output size by MaxResources (default 1000) per the LLD §4 caps.
//   - Per-reference Fetch failures degrade: the rest of the bundle is
//     emitted, the failure is recorded as a Warning string. Callers
//     wire warnings into an `OperationOutcome` extension; that wiring
//     is the builder's job, not this package's.
//
// What this package does NOT do:
//   - JSON canonicalization. Match bodies are passed through verbatim.
//   - Caching. The HydrationService implementation owns its TTL cache;
//     the SPI's CacheTTL() is exposed for it to honor.
//   - Bundle envelope assembly. The caller is responsible for wrapping
//     Match + Include into a FHIR Bundle.
package hydration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

// IncludeRule is one parsed `_include` or `_revinclude` parameter.
//
// SourceType is the FHIR resource type the rule applies to (e.g.,
// "Observation"). Empty matches every match resource.
//
// Param is the FHIR search parameter name (e.g., "subject", "patient").
// In v1 we use it as a JSON object key on the match resource for
// forward includes; for reverse includes the caller's Extractor is the
// authority on what the param means.
//
// Reverse=true selects `_revinclude` semantics.
type IncludeRule struct {
	SourceType string
	Param      string
	Reverse    bool
}

// ReferenceExtractor is the strategy that, given a resource and a rule,
// returns the references that rule wants hydrated. The default
// extractor walks JSON for forward includes; reverse includes require
// a caller-supplied extractor backed by a search index.
type ReferenceExtractor func(res spi.FhirResource, rule IncludeRule) ([]spi.FhirReference, error)

// Config tunes the hydrator.
type Config struct {
	// Service is the SPI HydrationService used for point Fetch. Required.
	Service spi.HydrationService

	// Extractor returns refs for an (resource, rule) pair. Optional —
	// nil falls back to DefaultExtractor for forward rules; reverse
	// rules without a custom extractor produce zero refs.
	Extractor ReferenceExtractor

	// MaxDepth caps recursive includes. The match set is depth 0;
	// each fetch level increments. Default 3.
	MaxDepth int

	// MaxResources caps total Match+Include output count. Default 1000.
	MaxResources int
}

// Hydrator is the in-process include/revinclude expander.
type Hydrator struct {
	service      spi.HydrationService
	extractor    ReferenceExtractor
	maxDepth     int
	maxResources int
}

// Result is the matched + included resource set the builder folds into
// the notification Bundle. Match entries become `searchEntry.mode = match`
// and Include entries become `searchEntry.mode = include` per FHIR
// searchset semantics.
type Result struct {
	Match    []spi.FhirResource
	Include  []spi.FhirResource
	Warnings []string
}

// Default caps per docs/low-level-design/subscriptions-engine.md §4.
const (
	defaultMaxDepth     = 3
	defaultMaxResources = 1000
)

// New constructs a Hydrator. Service is required; nil panics in Hydrate.
func New(cfg Config) *Hydrator {
	if cfg.MaxDepth <= 0 {
		cfg.MaxDepth = defaultMaxDepth
	}
	if cfg.MaxResources <= 0 {
		cfg.MaxResources = defaultMaxResources
	}
	if cfg.Extractor == nil {
		cfg.Extractor = DefaultExtractor
	}
	return &Hydrator{
		service:      cfg.Service,
		extractor:    cfg.Extractor,
		maxDepth:     cfg.MaxDepth,
		maxResources: cfg.MaxResources,
	}
}

// Hydrate walks the include rules over the match set and returns the
// matched + included resources. A nil HydrationService produces a
// loud failure rather than silently dropping rules.
func (h *Hydrator) Hydrate(ctx context.Context, matches []spi.FhirResource, rules []IncludeRule) (Result, error) {
	if h.service == nil {
		return Result{}, fmt.Errorf("hydration: HydrationService is required")
	}

	res := Result{Match: append([]spi.FhirResource(nil), matches...)}
	if len(rules) == 0 {
		return res, nil
	}

	// seen indexes every Type/ID already present in Match or Include
	// so we never refetch and never double-emit.
	seen := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		if m.ResourceType != "" && m.ID != "" {
			seen[m.ResourceType+"/"+m.ID] = struct{}{}
		}
	}

	// Frontier of resources whose outbound refs we still need to walk.
	// At depth 0 it's the match set; subsequent depths are the
	// resources fetched in the previous pass.
	frontier := matches
	for depth := 1; depth <= h.maxDepth && len(frontier) > 0; depth++ {
		next := make([]spi.FhirResource, 0, len(frontier))
		capped := false
	frontierLoop:
		for i := range frontier {
			src := frontier[i]
			for _, rule := range rules {
				// For forward includes, SourceType selects which match
				// resources contribute references. For reverse
				// includes, SourceType names the *target* resource type
				// to find — the filter doesn't apply to the source, so
				// every frontier resource is a potential pivot and the
				// caller's Extractor decides whether it matches.
				if !rule.Reverse && rule.SourceType != "" && rule.SourceType != src.ResourceType {
					continue
				}
				refs, err := h.extractor(src, rule)
				if err != nil {
					res.Warnings = append(res.Warnings,
						fmt.Sprintf("extract %s/%s param=%s: %v", src.ResourceType, src.ID, rule.Param, err))
					continue
				}
				for _, ref := range refs {
					if ref.ResourceType == "" || ref.ID == "" {
						continue
					}
					key := ref.ResourceType + "/" + ref.ID
					if _, dup := seen[key]; dup {
						continue
					}
					if len(res.Match)+len(res.Include) >= h.maxResources {
						res.Warnings = append(res.Warnings,
							fmt.Sprintf("hydration cap %d reached at depth %d; further references skipped",
								h.maxResources, depth))
						capped = true
						break frontierLoop
					}
					fetched, ferr := h.service.Fetch(ctx, ref)
					if ferr != nil {
						res.Warnings = append(res.Warnings,
							fmt.Sprintf("fetch %s: %v", key, ferr))
						// Record as seen so we don't retry on the
						// next depth via a different path.
						seen[key] = struct{}{}
						continue
					}
					seen[key] = struct{}{}
					res.Include = append(res.Include, fetched)
					next = append(next, fetched)
				}
			}
		}
		if capped {
			break
		}
		frontier = next
	}

	return res, nil
}

// DefaultExtractor walks the resource body's top-level JSON looking for
// the param key. It honors:
//
//	"<param>": {"reference": "Type/id"}
//	"<param>": [{"reference": "Type/id"}, ...]
//
// Anything else (FHIR contained references, canonical refs, identifier-
// only refs) is out of scope for the MVP — the caller can plug a richer
// Extractor for those.
func DefaultExtractor(res spi.FhirResource, rule IncludeRule) ([]spi.FhirReference, error) {
	if rule.Reverse {
		// Reverse includes need an inverse search; the default has
		// no way to satisfy them. Return empty rather than erroring
		// so the caller's mix of forward and reverse rules still
		// progresses — the reverse rule is simply a no-op without a
		// custom Extractor.
		return nil, nil
	}
	if len(res.Body) == 0 || rule.Param == "" {
		return nil, nil
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(res.Body, &generic); err != nil {
		return nil, fmt.Errorf("decode body: %w", err)
	}
	raw, ok := generic[rule.Param]
	if !ok {
		return nil, nil
	}
	return decodeReferenceField(raw)
}

func decodeReferenceField(raw json.RawMessage) ([]spi.FhirReference, error) {
	trim := strings.TrimSpace(string(raw))
	if trim == "" || trim == "null" {
		return nil, nil
	}
	switch trim[0] {
	case '{':
		ref, ok, err := decodeOneRef(raw)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, nil
		}
		return []spi.FhirReference{ref}, nil
	case '[':
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, fmt.Errorf("decode array: %w", err)
		}
		out := make([]spi.FhirReference, 0, len(arr))
		for _, item := range arr {
			ref, ok, err := decodeOneRef(item)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, ref)
			}
		}
		return out, nil
	default:
		// Scalars and other shapes are not references in v1.
		return nil, nil
	}
}

func decodeOneRef(raw json.RawMessage) (spi.FhirReference, bool, error) {
	var obj struct {
		Reference string `json:"reference"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return spi.FhirReference{}, false, fmt.Errorf("decode reference: %w", err)
	}
	if obj.Reference == "" {
		return spi.FhirReference{}, false, nil
	}
	parts := strings.SplitN(obj.Reference, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return spi.FhirReference{}, false, nil
	}
	return spi.FhirReference{ResourceType: parts[0], ID: parts[1]}, true, nil
}
