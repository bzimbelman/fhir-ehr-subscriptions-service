# ADR 0007: We stay inside the FHIR Subscriptions spec boundary

**Status.** Accepted.

**Reader's prerequisites.** Read `../../high-level-concept.md` (the Non-Goals section) and `../../architecture.md` (Out of Scope section). The spec page that anchors most of this is [`https://hl7.org/fhir/R5/subscription.html`](https://hl7.org/fhir/R5/subscription.html).

## Context

A FHIR Subscriptions server is small if you stay inside the spec and big if you don't. The spec defines:

- The `Subscription` resource and its lifecycle.
- The `SubscriptionTopic` resource, with `resourceTrigger`, `eventTrigger`, `notificationShape`, `canFilterBy`, and the matching languages (search-parameter expressions, FHIRPath).
- The `SubscriptionStatus` resource and the five notification types (`handshake`, `heartbeat`, `event-notification`, `query-status`, `query-event`).
- Four core channel types (`rest-hook`, `websocket`, `email`, `message`) plus an extensible binding for custom channels.
- The `subscription-notification` Bundle wire shape and the three payload types (`empty`, `id-only`, `full-resource`).
- The `$status`, `$events`, and `$get-ws-binding-token` operations.
- SMART on FHIR Backend Services authentication and the requirement that scopes are re-checked at delivery time.

During architecture review several proposed features were considered and removed for being outside the spec:

- **Signed payloads.** A common practice for generic webhook ecosystems (Stripe, GitHub, etc.) — the producer signs the payload with a shared secret, the consumer verifies the signature. The FHIR Subscriptions spec does not define a signature mechanism. Subscribers who need authenticity of payloads can verify the connection (TLS) and the source (the FHIR Subscriptions server's identity, presented via TLS); adding a project-private signature header would be a non-spec extension that no other Subscriptions server speaks.
- **Outbound HL7 v2 queries.** The proposal was that the server could query the EHR over HL7 v2 (e.g., `QRY^A19`) to pull additional context during hydration. HL7 v2 has its own standard for queries; it is not referenced by the FHIR Subscriptions spec. The architecture already covers EHR-side reads via three channels (FHIR REST, vendor proprietary APIs, vendor change feeds), all owned by the adapter. Adding HL7 v2 query support would expand the EHR surface without spec backing.
- **Project-bespoke matching DSL.** Recorded separately in [0006-no-cql-no-regex.md](0006-no-cql-no-regex.md).
- **Custom-named server-side filtering scopes.** The temptation is to invent additional `Subscription`-level filtering knobs the spec does not define. The right answer is to use the spec's `filterBy` and the topic's `canFilterBy` whitelist; if a deployment needs more, that lives on the subscriber side or in a downstream rules engine.

The pattern across these is: each was a generic "production system" reflex that doesn't apply when the contract is the FHIR Subscriptions spec. The spec is what subscribers depend on; if we extend the spec, subscribers depend on us instead, and the project becomes harder to replace.

## Decision

**The project stays inside the FHIR Subscriptions spec boundary.** Features beyond the spec are not added unless the architecture explicitly commits to them.

Specifically:

- **No project-private extensions to the wire shape.** The `Subscription` resource, the `SubscriptionTopic` resource, the `SubscriptionStatus` resource, the `subscription-notification` Bundle, the operation responses — all conform exactly to the spec.
- **No project-private extensions to the auth model.** SMART on FHIR Backend Services. Scopes re-checked at delivery time per the spec. No mTLS-only mode (removed during review). No OAuth client-credentials alternative (removed during review).
- **No project-private channel types.** The four spec-defined channel types are built in; custom channels are supported through the spec's extensible binding. Operators who write custom channels register their own Coding (their own `system` + `code`); they do not get to extend the standard `subscription-channel-type` system.
- **No matching beyond search-parameter expressions and FHIRPath.** Per [0006-no-cql-no-regex.md](0006-no-cql-no-regex.md).
- **No outbound vendor protocols invented for the engine's convenience.** EHR-side I/O is HL7 v2 inbound (over MLLP), FHIR REST (out via the adapter), and vendor-specific APIs / change feeds (the adapter's choice). The engine does not push EHR-side I/O patterns.
- **No payload signing.** TLS provides transport authenticity. Subscribers requiring stronger guarantees use channel-specific mechanisms (S/MIME for email, mutual TLS at the endpoint for HTTPS — both of which are subscriber-side configurations, not server-side payload signing).
- **No HL7 v2 outbound.** Outbound to the EHR is via the adapter's FHIR REST or vendor APIs. The MLLP path is inbound only.

When a feature is genuinely useful but outside the spec, the project's response is: support it on the subscriber side, support it via a custom channel, or propose it to HL7 for inclusion in a future spec version.

## Consequences

### Positive

- **Subscribers can replace this server with another spec-conformant server.** The contract is the spec, not the project. Subscribers depending on a project-private extension would be locked in; they are not, because the project does not offer one.
- **The codebase is small.** Each feature beyond the spec would be ongoing maintenance, ongoing test surface, and ongoing documentation. By staying inside the spec, the project's surface area equals the spec's surface area.
- **Conformance testing is meaningful.** Inferno and Touchstone (see [../domains/subscriptions-api.md](../domains/subscriptions-api.md#conformance-testing)) test against the spec. Their results are credible because the project doesn't have non-spec features that would skew them.
- **Future spec changes have a clear migration path.** When R6 publishes (or when the R4B Backport IG is updated), the project tracks the new spec exactly. There is no project-private behavior that has to be reconciled with the new spec.
- **Operators understand the bounds.** "What does this server do?" has a precise answer: "Everything the FHIR Subscriptions spec says it should, on the four built-in channel types, with the listed adapter-supplied EHR integrations."

### Negative

- **Some real-world subscriber asks are outside scope.** "Can the server sign the payload so I can verify it didn't come from a man-in-the-middle?" — the answer is "use TLS plus authentication of the server's identity." That is a less convenient answer than "yes, here is a signature header," but it is the spec's answer.
- **Operators can't ask the project to add a non-spec feature.** A facility that needs custom payload signing has to write a custom channel that does the signing on top of the standard payload. The project will not add a `payloadSignatureKey` field to `Subscription`.
- **The project does not solve every clinical-event delivery problem.** Subscribers who want richer matching (CQL, regex), cross-resource correlation, or vendor-specific payload shapes use a downstream rules engine fed by this server's notifications. We accept that the project is a bridge, not a complete clinical-rules platform.

### Neutral

- **Custom channels and adapter-contributed topics are still in scope.** Both are spec-supported extension points (the channel-type extensible binding; the adapter contributing topics through its manifest). They extend what the deployment can do without extending the spec itself.
- **Operator-supplied topics are still in scope.** The operator can author and load `SubscriptionTopic` resources tailored to their facility, but those topics use only the spec's matching languages and the spec's topic shape.
- **The decision is enforced by review, not by configuration.** There is no "spec strict mode" knob; the project simply does not implement non-spec features. PRs that propose them must include a reference to a spec citation or are declined.
