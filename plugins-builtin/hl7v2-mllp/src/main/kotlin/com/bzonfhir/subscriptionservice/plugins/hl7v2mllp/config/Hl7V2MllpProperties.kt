package com.bzonfhir.subscriptionservice.plugins.hl7v2mllp.config

import org.springframework.boot.context.properties.ConfigurationProperties

/**
 * Configurable knobs for the HL7 v2 MLLP ingest plugin (ticket #431).
 *
 * Property prefix: `subscription-service.ingest.hl7v2-mllp`. Operators
 * configure these via `application.yaml` or env vars in the usual
 * Spring-relaxed-binding way (e.g. `SUBSCRIPTION_SERVICE_INGEST_HL7V2_MLLP_PORT=2575`).
 *
 * ## Backward compatibility with the legacy `subscription-service.mllp.port` key
 *
 * The pre-#431 interface-engine read MLLP_PORT through
 * `${subscription-service.mllp.port:2575}`. Many tests in interface-engine
 * (admin controller tests, observability tests, worker tests) still set
 * THAT key on their `@DynamicPropertySource` to a random free port. To
 * keep those tests green without churning through every test class, the
 * auto-config (see `Hl7V2MllpAutoConfiguration`) reads the legacy key as
 * a default for [port] when the new key isn't set. The new key always
 * wins when present — operators migrating their deployments to the new
 * name don't have to delete the old one immediately.
 *
 * ## Why a ConfigurationProperties class and not plain @Value
 *
 * Plain `@Value("\${...}")` was fine when there was one knob (port).
 * Ticket #431 surfaces four (enabled / port / host / character-set);
 * having them all as fields on one bean (a) groups them in the IDE's
 * completion list under one namespace and (b) makes it cheap to add
 * more knobs in a follow-up without sprinkling @Value annotations
 * across the route builder.
 *
 * The class uses Spring Boot 3's constructor-binding mode for
 * @ConfigurationProperties: a single primary constructor with default
 * values per property. Spring detects the single-constructor shape and
 * binds the fields directly via that constructor — no setters required,
 * and no kotlin-noarg gymnastics needed.
 */
@ConfigurationProperties(prefix = "subscription-service.ingest.hl7v2-mllp")
data class Hl7V2MllpProperties(

    /**
     * Master enable switch. Default `true` because the receive-half of
     * the pipeline is the whole point of the interface-engine binary;
     * the only realistic reasons to disable it are:
     *
     *   - running the host purely as an admin-API service (no ingest),
     *   - a future deployment that uses a different IngestSource plugin
     *     instead (e.g. fhir-r4-polling) and wants the MLLP plugin off.
     *
     * When false, the auto-config skips bean registration entirely and
     * the route builder never gets added to the Camel context — no port
     * is bound, no thread pool started.
     */
    val enabled: Boolean = true,

    /**
     * TCP port the MLLP listener binds to. 2575 is the de-facto default
     * for HL7 v2 over MLLP in the IPF / hapi-test-panel community.
     * Override via `SUBSCRIPTION_SERVICE_INGEST_HL7V2_MLLP_PORT` env var
     * (Spring's relaxed binding maps that to this property).
     */
    val port: Int = 2575,

    /**
     * Interface to bind. Default `0.0.0.0` so the listener accepts
     * connections from any pod IP in the cluster (and from outside the
     * cluster when a NodePort / Service routes to it). Tighten to a
     * specific interface in single-tenant deployments where only one
     * EHR is expected to connect.
     */
    val host: String = "0.0.0.0",

    /**
     * Character set the MLLP component uses to decode inbound bytes.
     * HL7 v2 messages are ASCII by spec but UTF-8 is a safe superset
     * and matches what the legacy route accepted. The HL7v2 message
     * MSH-18 field can declare a different charset; switching at that
     * level is out of scope for this story.
     */
    val characterSet: String = "UTF-8",
)
