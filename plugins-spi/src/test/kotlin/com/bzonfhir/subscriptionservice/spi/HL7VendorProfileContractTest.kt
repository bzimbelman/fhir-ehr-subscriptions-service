package com.bzonfhir.subscriptionservice.spi

import com.bzonfhir.subscriptionservice.spi.meta.AuditEnrichmentRule
import com.bzonfhir.subscriptionservice.spi.meta.FhirMappingResult
import com.bzonfhir.subscriptionservice.spi.meta.PluginMeta
import com.bzonfhir.subscriptionservice.spi.meta.PluginSupplier
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

/**
 * Contract test for [HL7VendorProfile].
 *
 * The point of these tests isn't to exercise behaviour (the interfaces
 * have no implementation in this module) — it's to LOCK THE SHAPE.
 * If someone changes the interface in a binary-incompatible way, these
 * tests fail to compile. That's the whole bargain of plugins-spi: a
 * stable surface that consumers can build against.
 */
class HL7VendorProfileContractTest {

    @Test
    fun `HL7VendorProfile shape compiles and exposes profile metadata`() {
        val profile: HL7VendorProfile = object : HL7VendorProfile {
            override val meta = PluginMeta(
                id = "epic",
                version = "2024.1.0",
                schemaVersion = 1,
                supplier = PluginSupplier.FIRST_PARTY,
                description = "Epic vendor profile (test stub)",
            )

            override val supportedMessageTypes = setOf("ADT^A04", "ORM^O01", "ORU^R01")

            override val quirks = mapOf(
                "msh3-format" to "facility-shortcode-then-pipe",
                "empty-pid-strategy" to "synthesize-from-mrn",
            )

            override val auditEnrichments = listOf(
                AuditEnrichmentRule(field = "addOriginatingUser", source = "pv1.7"),
                AuditEnrichmentRule(field = "addPatientFacility", source = "msh.4"),
            )

            override fun mapMessageToFhir(raw: ByteArray, contentType: String): FhirMappingResult =
                FhirMappingResult(
                    bundleJson = """{"resourceType":"Bundle","type":"transaction","entry":[]}""",
                    warnings = listOf("test stub mapper — no actual transform performed"),
                )
        }

        // Identity surfaces correctly.
        assertThat(profile.meta.id).isEqualTo("epic")
        assertThat(profile.meta.supplier).isEqualTo(PluginSupplier.FIRST_PARTY)

        // Supported types are queryable.
        assertThat(profile.supportedMessageTypes).contains("ADT^A04")

        // Quirks + audit enrichments survive the round trip.
        assertThat(profile.quirks["msh3-format"]).isEqualTo("facility-shortcode-then-pipe")
        assertThat(profile.auditEnrichments).hasSize(2)
        assertThat(profile.auditEnrichments[0].field).isEqualTo("addOriginatingUser")

        // Mapping function is invocable and returns the documented shape.
        val result = profile.mapMessageToFhir(
            raw = "MSH|^~\\&|EPIC|...".toByteArray(),
            contentType = "application/hl7-v2",
        )
        assertThat(result.bundleJson).contains("\"resourceType\":\"Bundle\"")
        assertThat(result.warnings).hasSize(1)
    }
}
