package com.bzonfhir.subscriptionservice.spi

import com.bzonfhir.subscriptionservice.spi.meta.AuthDecision
import com.bzonfhir.subscriptionservice.spi.meta.PluginMeta
import com.bzonfhir.subscriptionservice.spi.meta.PluginSupplier
import com.bzonfhir.subscriptionservice.spi.meta.Principal
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

class AuthorizationDeciderContractTest {

    @Test
    fun `AuthorizationDecider shape compiles and supports allow deny abstain`() {
        val decider: AuthorizationDecider = object : AuthorizationDecider {
            override val meta = PluginMeta(
                id = "test-okta",
                version = "0.0.1",
                schemaVersion = 1,
                supplier = PluginSupplier.COMMERCIAL,
                description = "Test stub Okta-backed decider",
            )

            override fun decide(principal: Principal, action: String, resource: String): AuthDecision {
                if ("okta-admin" in principal.roles) return AuthDecision.Allow(reason = "okta admin role")
                if (action == "delete") return AuthDecision.Deny(reason = "non-admins cannot delete")
                return AuthDecision.Abstain
            }
        }

        val admin = Principal(name = "alice", roles = setOf("okta-admin"))
        val viewer = Principal(name = "bob", roles = setOf("viewer"))

        // Sealed-class `when` — the whole reason we made AuthDecision a sealed class.
        val adminDecision = decider.decide(admin, "delete", "Patient/123")
        val viewerDeleteDecision = decider.decide(viewer, "delete", "Patient/123")
        val viewerReadDecision = decider.decide(viewer, "read", "Patient/123")

        assertThat(adminDecision).isInstanceOf(AuthDecision.Allow::class.java)
        assertThat(viewerDeleteDecision).isInstanceOf(AuthDecision.Deny::class.java)
        assertThat(viewerReadDecision).isEqualTo(AuthDecision.Abstain)

        // Exhaustive sealed switch compiles. (Compiler would warn at a 4th branch.)
        val label: String = when (adminDecision) {
            is AuthDecision.Allow -> "allow:${adminDecision.reason}"
            is AuthDecision.Deny -> "deny:${adminDecision.reason}"
            AuthDecision.Abstain -> "abstain"
        }
        assertThat(label).startsWith("allow:")
    }
}
