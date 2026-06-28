package com.bzonfhir.subscriptionservice.interfaceengine.entitlements

import com.bzonfhir.subscriptionservice.interfaceengine.entitlements.config.LicenseProperties
import jakarta.annotation.PostConstruct
import org.springframework.beans.factory.annotation.Autowired
import org.springframework.boot.context.properties.EnableConfigurationProperties
import org.springframework.context.annotation.Bean
import org.springframework.context.annotation.Conditional
import org.springframework.context.annotation.ConditionContext
import org.springframework.context.annotation.Configuration
import org.springframework.core.type.AnnotatedTypeMetadata
import org.springframework.scheduling.annotation.Scheduled
import org.springframework.stereotype.Component

/**
 * Spring wiring for the entitlement runtime (ticket #460, Epic #428).
 *
 * Component scan picks this up via @Configuration. It does NOT register any
 * @Scheduled beans directly — the [EntitlementRefreshScheduler] holds the
 * scheduling annotation so a test can disable the refresh tick without
 * disturbing the holder bean itself.
 *
 * The web-facing 402-translator lives in [EntitlementMissingControllerAdvice];
 * that's a `@RestControllerAdvice` Spring picks up via component scan too.
 *
 * ## License-key gating (ticket #527)
 *
 * The `entitlementHolder` bean (and everything that depends on it) is
 * published ONLY when `subscription-service.license.key` is set AND
 * resolves to a non-blank value. Commercial plugins use
 * `@ConditionalOnBean(name = "entitlementHolder")` to gate their loading;
 * a FOSS deploy (no LICENSE_KEY) must NOT publish the bean or the
 * plugins load with empty entitlements and request-time
 * `@RequiresEntitlement` returns 402 — leaking the existence of
 * commercial endpoints into the discovery surface (Swagger, 404 vs 402).
 *
 * We use a custom `Condition` rather than `@ConditionalOnProperty` because
 * the latter treats an empty string as a present value (which is the most
 * common shape of an unset `LICENSE_KEY` env var — YAML produces `""` from
 * the `${LICENSE_KEY:}` default). The custom condition trims the resolved
 * property so whitespace-only typos (` `, `\t`) also degrade to FOSS.
 * Plain `@ConditionalOnExpression` would also work; using a named class
 * gives us a readable condition name in `--debug` startup output and a
 * single place to evolve the logic if we ever need to gate on additional
 * properties (e.g. allow-listed tier names).
 */
@Configuration
@EnableConfigurationProperties(LicenseProperties::class)
class EntitlementsAutoConfig {

    @Bean
    fun licenseClient(properties: LicenseProperties): LicenseClient =
        LicenseClient(licenseServerUrl = properties.serverUrl)

    @Bean
    @Conditional(LicenseKeyPresentCondition::class)
    fun entitlementHolder(
        properties: LicenseProperties,
        client: LicenseClient,
    ): EntitlementHolder = EntitlementHolder(
        properties = properties,
        fetcher = client::fetchEntitlements,
    )

    /**
     * Guard aspect only makes sense when the holder is wired. In FOSS mode
     * the commercial plugins that carry `@RequiresEntitlement` methods
     * never load, so there are no pointcut matches to gate — Spring AOP
     * without this aspect is a no-op for code that never registered.
     *
     * Uses the same [LicenseKeyPresentCondition] as the holder bean rather
     * than `@ConditionalOnBean(EntitlementHolder::class)` so the two gates
     * are evaluated independently in a single pass — no risk of the aspect
     * being registered against a stale view of the bean factory.
     */
    @Bean
    @Conditional(LicenseKeyPresentCondition::class)
    fun entitlementGuardAspect(
        holder: EntitlementHolder,
        properties: LicenseProperties,
    ): EntitlementGuardAspect = EntitlementGuardAspect(holder, properties)
}

/**
 * Custom [org.springframework.context.annotation.Condition] for
 * "operator has set a non-blank `subscription-service.license.key`."
 *
 * - Returns `false` when the property is missing OR the resolved value is
 *   blank (empty / whitespace-only).
 * - Returns `true` only for a real-looking key.
 *
 * The condition reads via `Environment.getProperty` so it sees every
 * Spring property source — env vars, command-line args, `application.yaml`,
 * `@TestPropertySource`, `ApplicationContextRunner.withPropertyValues`,
 * etc. — without us having to spell out the precedence.
 */
class LicenseKeyPresentCondition : org.springframework.context.annotation.Condition {
    override fun matches(context: ConditionContext, metadata: AnnotatedTypeMetadata): Boolean {
        val resolved = context.environment.getProperty("subscription-service.license.key")
        return !resolved.isNullOrBlank()
    }
}

/**
 * Periodic refresh tick. Pulled out of [EntitlementsAutoConfig] so a test
 * that wants the holder + aspect but NOT a background timer can exclude
 * just this component (`@SpringBootTest(properties = "...")` or a focused
 * `@TestConfiguration`).
 *
 * Gated on [LicenseKeyPresentCondition] (ticket #527) so a FOSS deploy
 * doesn't try to inject a missing holder. The scheduler is `@Component`-
 * scanned and its constructor requires `EntitlementHolder`; without this
 * gate the JVM would fail to start when no LICENSE_KEY is configured. We
 * use the same property condition as the holder bean itself (rather than
 * `@ConditionalOnBean(EntitlementHolder::class)`) because `Condition`
 * implementations are evaluated independently of bean-registration order
 * — scanning the scheduler before the auto-config's beans are registered
 * with `@ConditionalOnBean` could otherwise produce the wrong answer.
 */
@Component
@Conditional(LicenseKeyPresentCondition::class)
class EntitlementRefreshScheduler @Autowired constructor(
    private val holder: EntitlementHolder,
) {
    /** Hit the license server once at boot so the active set is populated before traffic arrives. */
    @PostConstruct
    fun onBoot() {
        holder.initialFetch()
    }

    /**
     * Periodic refresh. Default interval matches the UI's
     * `AUTO_REFRESH_MS = 12 * 60 * 60 * 1000`.
     *
     * `initialDelayString` defers the first scheduled fire so a slow license
     * server doesn't tank the readiness probe — the `@PostConstruct` boot
     * fetch already populates the set synchronously.
     */
    @Scheduled(
        fixedRateString = "#{T(java.time.Duration).ofHours(\${subscription-service.license.refresh-interval-hours:12}).toMillis()}",
        initialDelayString = "#{T(java.time.Duration).ofHours(\${subscription-service.license.refresh-interval-hours:12}).toMillis()}",
    )
    fun refreshTick() {
        holder.refresh()
    }
}
