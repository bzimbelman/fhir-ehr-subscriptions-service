/**
 * subscription-service plugin SPI (`plugins-spi`).
 *
 * Ticket #430, Epic #425 (the foundational story — every other story in
 * Epic #425 depends on the interfaces declared here).
 *
 * # What this module is
 *
 * The PUBLIC backend extension surface for subscription-service. Every
 * plugin a third party (or our own commercial arm, or the community)
 * ships against the engine implements one or more of the interfaces in
 * this package and gets discovered + bound by the runtime at boot.
 *
 * Seven backend SPI surfaces are defined here:
 *
 *   1. [com.bzonfhir.subscriptionservice.spi.HL7VendorProfile] —
 *      vendor profile manifest binding (Epic, Athena, Meditech…)
 *   2. [com.bzonfhir.subscriptionservice.spi.IngestSource] —
 *      pluggable inbound source (MLLP, REST polling, native APIs)
 *   3. [com.bzonfhir.subscriptionservice.spi.MessageSink] —
 *      pluggable outbound delivery beyond HAPI's own channels
 *   4. [com.bzonfhir.subscriptionservice.spi.SubscriptionFilter] —
 *      programmatic filter beyond FHIR criteria
 *   5. [com.bzonfhir.subscriptionservice.spi.AuthorizationDecider] —
 *      pluggable authz beyond OIDC roles
 *   6. [com.bzonfhir.subscriptionservice.spi.StorageBackend] —
 *      pluggable FHIR storage (HAPI JPA default; alternates allowed)
 *   7. [com.bzonfhir.subscriptionservice.spi.ObservabilityEnricher] +
 *      [com.bzonfhir.subscriptionservice.spi.AuditEventEnricher] —
 *      add fields to logs/metrics/AuditEvents
 *
 * The eighth surface from the master plan, `UiExtension`, lives in
 * TypeScript (`@bzonfhir/ui-extensions` on npm) and is NOT defined here.
 *
 * # What this module is NOT
 *
 * - **Not a runtime.** No Spring, no Camel, no IPF. The runtime that
 *   discovers and binds these implementations is `interface-engine/` and
 *   `hapi/auth/`. This module ships interfaces + value types only.
 * - **Not a HAPI repackage.** HAPI types referenced from the interfaces
 *   (`org.hl7.fhir.r4.model.*`) are `compileOnly` dependencies. Plugins
 *   bring their own HAPI on the classpath at their own pinned version,
 *   eliminating version skew between plugins and the runtime.
 * - **Not a built-in plugin host.** Built-in plugins live in
 *   `plugins-builtin/` (not yet created — comes in later stories).
 *
 * # Stability
 *
 * Everything in this module is **EXPERIMENTAL** at the time of v0.x.
 * The shape may change. See `README.md` for the full stability /
 * deprecation policy.
 *
 * # Why Kotlin + interfaces (not annotations)
 *
 * Annotations as plugin contracts (think Spring's `@Component`) tie
 * plugin lifetime to a particular Spring version and force plugin
 * authors to import our Spring. Plain interfaces are framework-neutral
 * and let third parties use whatever DI / lifecycle approach they
 * prefer.
 *
 * Kotlin over Java because every other backend module in this repo is
 * Kotlin and we want the interfaces to express nullable / non-null
 * shapes precisely. Plugin authors can still implement these from
 * Java — Kotlin interfaces are bytecode-compatible with Java consumers.
 */
package com.bzonfhir.subscriptionservice.spi
