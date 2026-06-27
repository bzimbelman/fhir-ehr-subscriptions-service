package com.bzonfhir.subscriptionservice.plugins.profileloader

import com.bzonfhir.subscriptionservice.plugins.profileloader.config.ProfileLoaderProperties
import org.springframework.boot.autoconfigure.AutoConfiguration
import org.springframework.boot.autoconfigure.condition.ConditionalOnMissingBean
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty
import org.springframework.boot.context.event.ApplicationReadyEvent
import org.springframework.boot.context.properties.EnableConfigurationProperties
import org.springframework.context.annotation.Bean
import org.springframework.context.event.EventListener

/**
 * Spring Boot auto-configuration for the profile-loader plugin
 * (ticket #435, Epic #425).
 *
 * Registered in
 * `META-INF/spring/org.springframework.boot.autoconfigure.AutoConfiguration.imports`.
 * When the plugin JAR is on the classpath and
 * `subscription-service.profiles.enabled` is `true` (default), the
 * auto-config:
 *
 *  1. Materializes a [ProfileLoaderProperties] bean from
 *     `application.yaml`.
 *  2. Creates a single [ProfileRegistry] bean that other parts of the
 *     runtime can inject.
 *  3. Creates a single [ProfileLoader] bean wired to the properties +
 *     registry.
 *  4. Listens for [ApplicationReadyEvent] and invokes
 *     [ProfileLoader.load] at that point — late enough that the
 *     application context is fully up, early enough that downstream
 *     code which depends on registered profiles sees them before the
 *     first inbound message arrives.
 *
 * ## Why ApplicationReadyEvent (not @PostConstruct on the loader)
 *
 * Loading at `@PostConstruct` time happens during bean creation, when
 * the application context isn't fully up — error reporting is awkward
 * (logging context not fully initialized, exceptions terminate the
 * whole boot rather than landing in the operator's INFO stream).
 * `ApplicationReadyEvent` runs AFTER the context is built and BEFORE
 * the host starts accepting traffic, which is exactly the window we
 * want.
 */
@AutoConfiguration
@ConditionalOnProperty(
    prefix = "subscription-service.profiles",
    name = ["enabled"],
    havingValue = "true",
    matchIfMissing = true,
)
@EnableConfigurationProperties(ProfileLoaderProperties::class)
open class ProfileLoaderAutoConfiguration {

    /**
     * Singleton in-memory profile catalog. `@ConditionalOnMissingBean`
     * so tests (and future commercial replacements) can supply a
     * different registry implementation if needed.
     */
    @Bean
    @ConditionalOnMissingBean
    open fun profileRegistry(): ProfileRegistry = ProfileRegistry()

    /**
     * The loader bean. Construction is side-effect-free; the actual
     * scan happens on [ApplicationReadyEvent] via [onApplicationReady].
     */
    @Bean
    @ConditionalOnMissingBean
    open fun profileLoader(
        properties: ProfileLoaderProperties,
        registry: ProfileRegistry,
    ): ProfileLoader = ProfileLoader(properties = properties, registry = registry)

    /**
     * Event listener that triggers the scan once the application
     * context is fully constructed. Pulls the loader out of the event's
     * context (rather than holding a field reference) so a test that
     * uses an ApplicationContextRunner can verify the listener wires
     * up correctly without instantiating this auto-config class.
     */
    @EventListener
    open fun onApplicationReady(event: ApplicationReadyEvent) {
        val ctx = event.applicationContext
        val loader = ctx.getBean(ProfileLoader::class.java)
        loader.load()
    }
}
