package com.bzonfhir.subscriptionservice.interfaceengine.entitlements

import com.bzonfhir.subscriptionservice.interfaceengine.entitlements.config.LicenseProperties
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.api.Assertions.assertThatThrownBy
import org.junit.jupiter.api.Test
import org.springframework.aop.aspectj.annotation.AspectJProxyFactory
import org.springframework.stereotype.Component

/**
 * AOP-level test for [RequiresEntitlement] (ticket #460, Epic #428).
 *
 * Builds a Spring [AspectJProxyFactory] directly so we don't pay the cost of
 * a full @SpringBootTest. The aspect is instantiated against a stub holder +
 * stub properties; the proxy factory wraps a bean with the aspect; we
 * exercise the proxied method.
 *
 * Coverage:
 *   - present entitlement -> body runs
 *   - missing entitlement, fail-mode DENY -> EntitlementMissingException
 *   - missing entitlement, fail-mode LOG -> body runs
 *   - disabled license -> body runs unconditionally
 *   - methods WITHOUT the annotation are not intercepted
 */
class RequiresEntitlementAspectTest {

    /** A test bean with a couple of annotated methods. */
    @Component
    open class GatedService {
        @RequiresEntitlement("audit.export.iti20")
        open fun gatedAuditExport(): String = "exported"

        @RequiresEntitlement("alerting.pagerduty")
        open fun gatedPagerduty(): String = "paged"

        /** Same shape, no annotation — must always pass through. */
        open fun ungated(): String = "open"
    }

    private fun proxiedService(
        holder: EntitlementHolder,
        properties: LicenseProperties,
    ): GatedService {
        val target = GatedService()
        val factory = AspectJProxyFactory(target)
        factory.addAspect(EntitlementGuardAspect(holder, properties))
        return factory.getProxy()
    }

    private fun holderWith(set: Collection<String>): EntitlementHolder {
        val props = LicenseProperties(enabled = true, key = "x", cachePath = "/dev/null")
        val response = VerifiedLicenseResponse(
            entitlements = set.toList(),
            tierName = "pro",
            expiresAt = "2027-06-28T00:00:00Z",
            signature = "x",
            signatureKid = "kid-1",
        )
        val h = EntitlementHolder(
            properties = props.copy(cachePath = java.nio.file.Files.createTempFile("entitlements", ".json").toString()),
            fetcher = { response },
        )
        h.initialFetch()
        return h
    }

    @Test
    fun `granted entitlement lets the method body run`() {
        val holder = holderWith(setOf("audit.export.iti20"))
        val props = LicenseProperties(enabled = true, key = "x", cachePath = "/dev/null")
        val svc = proxiedService(holder, props)

        assertThat(svc.gatedAuditExport()).isEqualTo("exported")
    }

    @Test
    fun `missing entitlement under DENY throws EntitlementMissingException`() {
        val holder = holderWith(setOf("alerting.pagerduty"))
        val props = LicenseProperties(
            enabled = true, key = "x", cachePath = "/dev/null",
            failMode = LicenseProperties.FailMode.DENY,
        )
        val svc = proxiedService(holder, props)

        assertThatThrownBy { svc.gatedAuditExport() }
            .isInstanceOf(EntitlementMissingException::class.java)
            .hasMessageContaining("audit.export.iti20")
    }

    @Test
    fun `missing entitlement under LOG lets the method body run`() {
        val holder = holderWith(emptySet())
        val props = LicenseProperties(
            enabled = true, key = "x", cachePath = "/dev/null",
            failMode = LicenseProperties.FailMode.LOG,
        )
        val svc = proxiedService(holder, props)

        assertThat(svc.gatedAuditExport()).isEqualTo("exported")
    }

    @Test
    fun `disabled license lets every annotated method run`() {
        val holder = holderWith(emptySet())
        val props = LicenseProperties(
            enabled = false, key = "x", cachePath = "/dev/null",
            failMode = LicenseProperties.FailMode.DENY,
        )
        val svc = proxiedService(holder, props)

        assertThat(svc.gatedAuditExport()).isEqualTo("exported")
        assertThat(svc.gatedPagerduty()).isEqualTo("paged")
    }

    @Test
    fun `unannotated methods are not intercepted`() {
        val holder = holderWith(emptySet())
        val props = LicenseProperties(
            enabled = true, key = "x", cachePath = "/dev/null",
            failMode = LicenseProperties.FailMode.DENY,
        )
        val svc = proxiedService(holder, props)

        // Empty set + DENY: the gated method would throw, but ungated() must
        // ALWAYS pass through regardless of the active set.
        assertThat(svc.ungated()).isEqualTo("open")
    }

    @Test
    fun `presence of a different entitlement still blocks the gated call`() {
        val holder = holderWith(setOf("some.other.feature"))
        val props = LicenseProperties(
            enabled = true, key = "x", cachePath = "/dev/null",
            failMode = LicenseProperties.FailMode.DENY,
        )
        val svc = proxiedService(holder, props)

        assertThatThrownBy { svc.gatedAuditExport() }
            .isInstanceOf(EntitlementMissingException::class.java)
    }
}
