package com.bzonfhir.subscriptionservice.interfaceengine.entitlements

import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.springframework.boot.test.context.runner.ApplicationContextRunner

/**
 * Conditional-wiring tests for [EntitlementsAutoConfig] (ticket #527).
 *
 * ## The defect this guards against
 *
 * Before #527, [EntitlementsAutoConfig] published the `entitlementHolder`
 * bean **unconditionally**: even with no `LICENSE_KEY` (i.e. a FOSS deploy
 * of the engine), the bean existed in the context with an empty entitlement
 * set. Commercial plugins gate their auto-configs with
 * `@ConditionalOnBean(name = "entitlementHolder")` to decide whether to
 * load — they intend the bean's presence to signal "this is a commercial
 * engine." With the always-on bean, the gate succeeded in FOSS mode, the
 * plugin loaded, registered routes/controllers, and request-time
 * `@RequiresEntitlement` returned HTTP 402. That LEAKED the existence of
 * commercial endpoints into the FOSS discovery surface (Swagger / 404 vs
 * 402) — exactly the disclosure the gate was designed to prevent.
 *
 * ## The fix (Option A from #527)
 *
 * Gate the entire commercial-runtime wiring (`entitlementHolder`,
 * `entitlementGuardAspect`, `EntitlementRefreshScheduler`) on
 * `subscription-service.license.key` being set AND non-empty. Now:
 *
 *   - No key (or `key=""`) -> no `entitlementHolder` bean ->
 *     `@ConditionalOnBean(name = "entitlementHolder")` fails ->
 *     commercial plugins do not load -> their endpoints 404, not 402.
 *   - Key set to a non-empty value -> beans wire normally.
 *
 * ## Test shape
 *
 * `ApplicationContextRunner` boots a minimal context with just
 * [EntitlementsAutoConfig] and asserts on the presence / absence of the
 * entitlement beans for the three FOSS-vs-commercial property cases
 * called out in the ticket spec.
 */
class EntitlementsAutoConfigConditionalTest {

    // Register both EntitlementsAutoConfig (which holds the @Bean methods)
    // AND EntitlementRefreshScheduler explicitly. The runner doesn't perform
    // a component scan — without listing the scheduler here it wouldn't be
    // considered for conditional registration even in commercial mode.
    private val contextRunner = ApplicationContextRunner()
        .withUserConfiguration(
            EntitlementsAutoConfig::class.java,
            EntitlementRefreshScheduler::class.java,
        )

    @Test
    fun `no license key configured - entitlementHolder bean is NOT in context (FOSS mode)`() {
        // No property -> no bean. Default is FOSS-safe.
        contextRunner.run { ctx ->
            assertThat(ctx).hasNotFailed()
            assertThat(ctx).doesNotHaveBean("entitlementHolder")
            assertThat(ctx).doesNotHaveBean(EntitlementHolder::class.java)
            // Aspect + scheduler depend on the holder; they must also stay out.
            assertThat(ctx).doesNotHaveBean("entitlementGuardAspect")
            assertThat(ctx).doesNotHaveBean(EntitlementGuardAspect::class.java)
            assertThat(ctx).doesNotHaveBean(EntitlementRefreshScheduler::class.java)
        }
    }

    @Test
    fun `empty license key string - entitlementHolder bean is NOT in context (FOSS mode)`() {
        // Operators commonly inject `LICENSE_KEY` env var; if the var is
        // unset, the YAML default `${LICENSE_KEY:}` produces an empty
        // string here. Empty must behave identically to "no property set".
        contextRunner
            .withPropertyValues("subscription-service.license.key=")
            .run { ctx ->
                assertThat(ctx).hasNotFailed()
                assertThat(ctx).doesNotHaveBean("entitlementHolder")
                assertThat(ctx).doesNotHaveBean(EntitlementHolder::class.java)
                assertThat(ctx).doesNotHaveBean("entitlementGuardAspect")
                assertThat(ctx).doesNotHaveBean(EntitlementGuardAspect::class.java)
                assertThat(ctx).doesNotHaveBean(EntitlementRefreshScheduler::class.java)
            }
    }

    @Test
    fun `non-empty license key - entitlementHolder bean IS in context (commercial mode)`() {
        contextRunner
            .withPropertyValues(
                "subscription-service.license.key=some-license-key",
                // Stub the cache path so the refresh scheduler's PostConstruct
                // doesn't try to write to /home/<user> in CI.
                "subscription-service.license.cache-path=${createTempCachePath()}",
                // Disable network: the scheduler's @PostConstruct will run
                // initialFetch() which would call out to the license server
                // without this. enabled=false short-circuits before any HTTP.
                "subscription-service.license.enabled=false",
            )
            .run { ctx ->
                assertThat(ctx).hasNotFailed()
                assertThat(ctx).hasBean("entitlementHolder")
                assertThat(ctx).hasSingleBean(EntitlementHolder::class.java)
                assertThat(ctx).hasBean("entitlementGuardAspect")
                assertThat(ctx).hasSingleBean(EntitlementGuardAspect::class.java)
                assertThat(ctx).hasSingleBean(EntitlementRefreshScheduler::class.java)
            }
    }

    @Test
    fun `whitespace-only license key - entitlementHolder bean is NOT in context`() {
        // Defence in depth: a key like " " or "\t" is a deployment typo,
        // not a real license. The condition should treat it as FOSS too.
        // (The holder's own runtime already does this via key.isBlank()
        // inside runFetch() — the bean-conditional makes the same call
        // at wiring time so commercial plugins don't load.)
        contextRunner
            .withPropertyValues("subscription-service.license.key=   ")
            .run { ctx ->
                assertThat(ctx).hasNotFailed()
                assertThat(ctx).doesNotHaveBean("entitlementHolder")
            }
    }

    private fun createTempCachePath(): String =
        java.nio.file.Files.createTempFile("entitlements-conditional-test", ".json").toString()
}
