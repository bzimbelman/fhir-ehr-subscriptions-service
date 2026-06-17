# ADR 0006: Subscription matching uses only FHIR search-parameter expressions and FHIRPath

**Status.** Accepted.

**Reader's prerequisites.** Read [../domains/topic-matcher.md](../domains/topic-matcher.md) (section "Matching expression languages") and `../../architecture.md` ("Matching expression languages").

## Context

Subscription matching needs an expression language. Two layers want one:

1. The Topic Matcher's evaluation of `SubscriptionTopic.resourceTrigger.queryCriteria.previous` and `.current`, plus `fhirPathCriteria`, when deciding whether a `resource_changes` row matches a topic.
2. The Subscriptions Engine's evaluation of `Subscription.filterBy` for per-subscription filtering against an `ehr_events` row.

Several languages were considered:

- **FHIR search-parameter expressions.** The string form a subscriber would write on a FHIR search URL — `status=active`, `subject=Patient/123`, `code=http://loinc.org|1234-5`. Spec-defined for both `SubscriptionTopic` (in `queryCriteria`) and `Subscription` (in `filterBy`). [`https://hl7.org/fhir/R5/search.html`](https://hl7.org/fhir/R5/search.html).
- **FHIRPath.** Path-based traversal expressions over a FHIR resource. Spec-defined on `SubscriptionTopic.resourceTrigger.fhirPathCriteria` and on `Subscription.filterBy` (where `filterParameter` is a FHIRPath).
- **CQL (Clinical Quality Language).** A clinical-rules language with a much larger surface than FHIRPath. Used in CDS Hooks and in some quality-measure systems. Not part of the FHIR Subscriptions spec.
- **Regular expressions.** General-purpose pattern matching. Not part of the spec.
- **A project-bespoke DSL.** A custom language designed specifically for subscription matching.

The FHIR Subscriptions spec is explicit about which languages topic authors and subscribers use: search-parameter expressions and FHIRPath. The two layers above need exactly the languages the spec defines.

Adding CQL would mean shipping a CQL evaluator in the server (CQL evaluators are large and have their own dependency chain), training topic authors and subscribers in a clinical-rules language they would not otherwise need, and creating a divergence from the spec — every other Subscriptions server implementation handles only the spec's languages, and topic resources written for this server would be incompatible with other servers.

Adding regex would create a non-spec extension. Topic authors would expect regex to do things FHIRPath does not (e.g., `Observation.code.coding.code matches /^[A-Z][0-9]+$/`). The minute regex is offered, the project owns the regex compatibility surface (which regex flavor — POSIX, PCRE, ECMAScript? Are anchors required? Are lookaheads supported?). Maintenance burden is high; spec compliance becomes harder to claim.

Adding a project-bespoke DSL would lock topic authors and subscribers into our DSL, fragmenting the topic catalog ecosystem. A topic written for this server would not be portable to any other Subscriptions server.

## Decision

**Subscription matching uses only the two languages the spec defines: FHIR search-parameter expressions and FHIRPath.** No CQL. No regex. No project-bespoke DSL.

For each language, the matcher supports the subset that matters for change-detection topics:

**FHIR search-parameter expressions.** Token equality (`=`, `:not`); reference equality (`=`, `:identifier`); string equality and `:contains`; date comparators (`eq`, `ne`, `gt`, `lt`, `ge`, `le`); `:missing`; `:in` against ValueSets pre-loaded into the topic catalog. Anything outside this subset is rejected at catalog-load time.

**FHIRPath.** Sandboxed evaluation with a per-evaluation wall-clock timeout (default 100 ms), a node-traversal limit, no I/O, and a deny-list for non-deterministic functions. `now()` and `today()` are stamped at evaluation start; nothing else with side effects is in scope.

A topic with a malformed search-parameter expression or an uncompilable FHIRPath is rejected at catalog-load time and never becomes active. A FHIRPath evaluation timeout or runtime error against a single resource skips that topic for that resource, increments a per-topic metric, and logs a debug entry. Persistent topic-level failure is operator-visible.

## Consequences

### Positive

- **Spec compliance is unambiguous.** The matcher accepts the exact languages the FHIR Subscriptions spec defines. Topics and subscriptions written against this server are portable.
- **Topic authors know the languages.** Anyone authoring `SubscriptionTopic` resources for the FHIR Subscriptions ecosystem knows search-parameter expressions and FHIRPath. We do not introduce new syntax.
- **Sandboxing is feasible.** Both languages have well-understood evaluation semantics and bounded cost (search-parameter evaluation walks a fixed extraction FHIRPath; FHIRPath has a known cost model with a timeout). A regex or CQL surface would be much larger.
- **Maintenance burden is bounded.** Two evaluators, both spec-defined, both with public test vectors. The matcher's correctness can be tested against the spec's own conformance materials.
- **The matcher's `core/filter` module is shared.** Both the Topic Matcher and the `$events` historical replay use the same evaluator for both languages. There is one place to fix evaluation bugs.

### Negative

- **Some operator-desired conditions cannot be expressed.** "An Observation whose `valueQuantity.value` matches a pattern that crosses three resources" might be expressible in CQL but not in FHIRPath alone. Such topics are out of scope for this server. We accept this — the value of staying inside the spec outweighs the value of supporting one-off complex topics.
- **No regex for string fields.** A topic that wants "any DiagnosticReport whose conclusion contains pattern X" must use FHIRPath's `matches()` if the spec exposes it, or settle for `contains` semantics. We do not extend FHIRPath with custom regex.
- **The supported-subset-of-search rejects some valid spec syntax.** Chained references, `:above`/`:below` hierarchical token modifiers, `_text` / `_content` free-text semantics — all spec-valid, all out of scope. A topic that uses them is rejected at catalog-load with an operator-visible error. This is a deliberate trade for evaluation cost and complexity.

### Neutral

- **The spec may grow.** If a future spec version adds a language or a search-parameter feature, the project decides at that time whether to support it. The current decision is "exactly what the spec defines today, in a usable subset."
- **Operator-supplied topics are still constrained.** An operator who wants to load a topic that uses a non-spec language has no path. Operators that need richer matching beyond what the spec offers are pointed at a downstream rules engine fed by this server's notifications, not at extending this server's matcher.
- **The decision applies to topic-side and subscription-side filter expressions equally.** `Subscription.filterBy` uses the same evaluator with the same subset.
