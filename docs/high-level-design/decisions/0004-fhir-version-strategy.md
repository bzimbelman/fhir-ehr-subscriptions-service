# ADR 0004: FHIR version strategy

**Status.** Accepted.

**Reader's prerequisites.** Read `../../architecture.md` (section "FHIR Version Strategy" — canonical) and [../contracts/subscriber-api.md](../contracts/subscriber-api.md).

## Context

FHIR Subscriptions span three published versions of the spec right now, and ignoring that creates upgrade pain later. Status as of 2026-06 (verified during architecture review):

- **FHIR R4** — predates the topic-based redesign of Subscriptions. Production EHRs and subscribers overwhelmingly speak R4. The **Subscriptions R5 Backport IG** (v1.1.0, STU, 2023-01) layers topic-based functionality onto R4 via extensions and profiles. See [`https://hl7.org/fhir/uv/subscriptions-backport/`](https://hl7.org/fhir/uv/subscriptions-backport/).
- **FHIR R4B** — the bridge release. Closer to R5 semantics, easier to map cleanly. The Backport IG aligns more cleanly with R4B than pure R4.
- **FHIR R5** — current published release. Topic-based subscriptions are native. `SubscriptionTopic` is a first-class resource. See [`https://hl7.org/fhir/R5/subscription.html`](https://hl7.org/fhir/R5/subscription.html).
- **FHIR R6** — in **first full ballot** (not yet published). Maintains the four core channel types and the extensible-binding model for custom channels.

Two reflexive options are wrong:

- **R4-only.** Locks the project out of R5 / R6 features and ties the codebase to backport semantics indefinitely. The internal model would be R4-shaped, and anything R5+ adds (e.g., refinements to `SubscriptionStatus` semantics) requires retrofit each time.
- **R5-only.** Cuts the project off from today's subscribers. The R4 install base is too large to ignore.

The right shape is to internalize one semantically clean model and translate at the wire to whichever version each subscriber speaks.

## Decision

- **Internal model is R5-shaped.** Domain types in `core` follow R5 semantics (topic-based, `SubscriptionStatus` resource, typed notification bundles). This is forward-compatible with R6 — most R6 changes are additive.
- **Subscriber-facing surface is multi-version**, served by a version-shim layer (`api/version-shim` per the architecture):
  - **R4B Backport IG** — the **primary subscriber-facing surface** today, because most subscribers are R4.
  - **R5 native** — for forward-looking subscribers.
  - **R6** — added once R6 publishes; tracked on a ballot-tracking branch in the meantime; not implemented against ballot drafts as if they were stable.
- **One subscription, one negotiated version.** Subscribers negotiate FHIR version on subscription create (via `Accept` / `Content-Type` headers). The server records the version on the `subscriptions` row and emits notifications for that subscription in that version for the life of the subscription. Internal storage is version-neutral (R5-shaped); shim translation runs on the way in and on the way out.
- **CapabilityStatement advertises supported versions** so subscribers can discover them.
- **R6 readiness checklist** lives in a separate doc once R6 publishes. The Channel SPI and Adapter SPI are designed to need no changes when R6 lands — R6 keeps the same channel-type extensibility model.

## Consequences

### Positive

- **Today's subscribers work.** R4B Backport on the wire is what most production subscribers speak. They are not blocked on R5 adoption.
- **Tomorrow's subscribers work.** R5 native is supported alongside R4B.
- **The internal model is stable.** Domain code, the engine, the channels, and the adapter all speak one shape. Version-handling complexity is concentrated at the API boundary.
- **R6 is a bounded change.** When R6 publishes, the change is "add an R6 representation to the version shim and update the `CapabilityStatement`." It is not a refactor of the engine or the storage layer.
- **Subscriber stability.** A subscriber's negotiated version is fixed on the `subscriptions` row. The subscriber does not see version drift mid-subscription; if they want a different version, they recreate the subscription.

### Negative

- **The version shim is real code that must be maintained.** It is the place that has to track each spec version's representation faithfully. Bugs in the shim are visible to subscribers. Shim test coverage has to be high. (Inferno + Touchstone — see [../domains/subscriptions-api.md](../domains/subscriptions-api.md#conformance-testing) — provide external conformance pressure.)
- **Some R5 features cannot be expressed in R4B.** Where the spec versions differ semantically (a field added in R5 that R4B has no representation for), the shim documents the truncation. Subscribers on R4B see the R4B-shaped subset; subscribers on R5 see the full R5 shape. This is the right trade — refusing to support R4B because it is less expressive than R5 would lock out the install base.
- **Two specs to track.** The team monitors HL7 work on R4B Backport, R5, and R6. Updates to the spec require corresponding updates to the shim.

### Neutral

- **Internal storage is version-neutral.** `subscriptions.resource` and `subscription_topics.resource` store the R5-shaped representation; the shim converts to/from on the wire. Migrating the internal representation in the future (for example, if R6 adds something the R5 shape can't carry) is an internal migration of `subscription_topics.resource` plus a new shim version; subscriber-facing behavior is unchanged.
- **The decision is independent of the implementation language.** The shim is a code component; whichever language the project uses, it is the same shape.
- **Conformance bar is external.** Inferno and Touchstone test against the spec. The project's "support R4B" and "support R5" claims are claims that the shim passes those external tools' relevant suites, not project-internal claims.
