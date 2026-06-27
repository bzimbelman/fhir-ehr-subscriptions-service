package com.bzonfhir.subscriptionservice.spi

import com.bzonfhir.subscriptionservice.spi.meta.AuditEnrichmentRule
import com.bzonfhir.subscriptionservice.spi.meta.FhirMappingResult
import com.bzonfhir.subscriptionservice.spi.meta.PluginMeta

/**
 * SPI #1 — Vendor profile binding.
 *
 * A vendor profile bundles everything specific to one EHR vendor's
 * dialect of the world: which message types they emit, their quirks,
 * the StructureMaps that turn their HL7 into FHIR (or their REST blobs
 * into FHIR), the audit-event enrichments they want, and a regression
 * test suite. See master plan §4.3 for the manifest shape.
 *
 * A profile can be:
 *
 *  - **100% declarative** — `manifest.yaml` + StructureMaps + sample
 *    messages, loaded by the YAML profile loader (ticket #435). The
 *    loader synthesizes an [HL7VendorProfile] implementation from the
 *    YAML at runtime; the third party writes no Kotlin/Java.
 *  - **Mostly declarative with one or two custom hooks** — a JAR drop-in
 *    that implements [HL7VendorProfile] directly, typically because the
 *    vendor has weird parsing requirements the YAML schema doesn't
 *    cover yet.
 *
 * # Lifecycle
 *
 * The runtime resolves which profile to activate based on the inbound
 * message's `sourceSystem` (MSH-3 for HL7 v2; tenant config for REST
 * sources). When a profile is activated, the runtime walks the
 * [supportedMessageTypes], the [quirks], and the [auditEnrichments] at
 * registration time and binds them into the pipeline. [mapMessageToFhir]
 * is called once per inbound message during the worker's transform
 * stage.
 *
 * # Stability: EXPERIMENTAL
 */
interface HL7VendorProfile {

    /**
     * Identity. The `id` here typically matches the profile YAML's
     * `profile.id` (`"epic"`, `"athena"`, `"meditech"`).
     */
    val meta: PluginMeta

    /**
     * The HL7 v2 (or REST) message types the profile can handle, in the
     * canonical `EVENT^TRIGGER` form (`"ADT^A04"`, `"ORM^O01"`). For
     * non-HL7 sources this contains the source-type strings the profile
     * declares in its YAML (`"athena-changed-patients"`).
     *
     * The runtime uses this set to decide whether the profile is a
     * candidate for an incoming message before invoking [mapMessageToFhir].
     */
    val supportedMessageTypes: Set<String>

    /**
     * Vendor-specific deviations from the standard. Each key MUST be
     * one of the well-known quirk strategy identifiers the runtime
     * implements (`"msh3-format"`, `"empty-pid-strategy"`,
     * `"attachment-encoding"`). Unknown keys cause profile loading to
     * fail with a clear "this profile is asking for a quirk this engine
     * version doesn't implement" error — preventing silent skipping of
     * a quirk the profile considers mandatory. See master plan §4.3.
     */
    val quirks: Map<String, String>

    /**
     * The audit-event enrichments declared by the profile's manifest.
     * Applied by the runtime's audit pipeline whenever this profile is
     * the originator of the message being audited. See [AuditEventEnricher]
     * and master plan §4.3 for how these interact with the AuditEvent
     * interceptor.
     */
    val auditEnrichments: List<AuditEnrichmentRule>

    /**
     * Map one inbound message to a FHIR Bundle (JSON).
     *
     * Called by the worker for each message whose `messageType` is in
     * [supportedMessageTypes]. The implementation is expected to:
     *
     *  - Apply any [quirks] before parsing (e.g. strip vendor-specific
     *    prefixes from binary segments).
     *  - Run the StructureMap (or vendor-specific code) that produces a
     *    Bundle.
     *  - Stamp provenance (`Provenance` resources) referencing the
     *    sourceSystem + sourceId.
     *  - Return the Bundle as JSON plus any non-fatal warnings.
     *
     * Throwing here lands the message in the dead-letter queue. Return
     * an empty bundle + a warning if the message was intentionally
     * dropped (`"profile dropped: ADT^A60 retraction message"`).
     *
     * @param raw The wire bytes as they arrived. Identical to
     *   `PipelineMessage.raw`.
     * @param contentType The IANA media type or well-known string the
     *   ingest stamped on the message (`"application/hl7-v2"`,
     *   `"application/fhir+json"`).
     */
    fun mapMessageToFhir(raw: ByteArray, contentType: String): FhirMappingResult
}
