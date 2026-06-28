package com.bzonfhir.subscriptionservice.interfaceengine.entitlements

import com.bzonfhir.subscriptionservice.interfaceengine.entitlements.config.LicenseProperties
import jakarta.annotation.PostConstruct
import org.springframework.beans.factory.annotation.Autowired
import org.springframework.boot.context.properties.EnableConfigurationProperties
import org.springframework.context.annotation.Bean
import org.springframework.context.annotation.Configuration
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
 */
@Configuration
@EnableConfigurationProperties(LicenseProperties::class)
class EntitlementsAutoConfig {

    @Bean
    fun licenseClient(properties: LicenseProperties): LicenseClient =
        LicenseClient(licenseServerUrl = properties.serverUrl)

    @Bean
    fun entitlementHolder(
        properties: LicenseProperties,
        client: LicenseClient,
    ): EntitlementHolder = EntitlementHolder(
        properties = properties,
        fetcher = client::fetchEntitlements,
    )

    @Bean
    fun entitlementGuardAspect(
        holder: EntitlementHolder,
        properties: LicenseProperties,
    ): EntitlementGuardAspect = EntitlementGuardAspect(holder, properties)
}

/**
 * Periodic refresh tick. Pulled out of [EntitlementsAutoConfig] so a test
 * that wants the holder + aspect but NOT a background timer can exclude
 * just this component (`@SpringBootTest(properties = "...")` or a focused
 * `@TestConfiguration`).
 */
@Component
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
