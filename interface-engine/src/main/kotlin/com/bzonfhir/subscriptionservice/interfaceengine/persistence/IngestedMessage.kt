package com.bzonfhir.subscriptionservice.interfaceengine.persistence

import jakarta.persistence.Column
import jakarta.persistence.Entity
import jakarta.persistence.EnumType
import jakarta.persistence.Enumerated
import jakarta.persistence.GeneratedValue
import jakarta.persistence.GenerationType
import jakarta.persistence.Id
import jakarta.persistence.Table
import jakarta.persistence.UniqueConstraint
import org.hibernate.annotations.JdbcTypeCode
import org.hibernate.type.SqlTypes
import java.time.OffsetDateTime

/**
 * Durable inbound row for the interface engine (Epic #378, ticket #380).
 *
 * One row = one message we received from any source protocol. The table is
 * the single source of truth for the inbound side of the pipeline:
 *
 *   - the receive route INSERTs (idempotently via [sourceSystem]+[sourceId])
 *     and ACKs the sender before doing any downstream work (#381),
 *   - the async worker (#382) polls by [status] + [nextAttemptAt] to drive
 *     transform + HAPI delivery,
 *   - operators inspect [status], [attemptCount], [lastError] for triage.
 *
 * Enum mapping uses Hibernate 6.5+'s [SqlTypes.NAMED_ENUM] so the Kotlin
 * enums round-trip cleanly against the Postgres ENUM types created by V002.
 * Without it Hibernate would try to bind enum values as VARCHAR, which
 * fails the table's column type check (ENUM != VARCHAR in Postgres).
 *
 * Schema invariants:
 *   - [id] is server-generated (BIGSERIAL); never set by callers.
 *   - [receivedAt] defaults at the database level — leaving the field null
 *     when saving a fresh row is fine, the column has DEFAULT now().
 *   - [status] defaults to RECEIVED at the DB level; we mirror that here.
 *   - [sourceSystem] + [sourceId] together form the idempotency key.
 */
@Entity
@Table(
    name = "ingested_messages",
    uniqueConstraints = [
        UniqueConstraint(
            name = "ingested_messages_source_system_source_id_key",
            columnNames = ["source_system", "source_id"],
        ),
    ],
)
// Every constructor parameter has a default value so that Kotlin generates
// the no-arg secondary constructor Hibernate needs for reflective row
// instantiation. (Equivalent to applying the kotlin-noarg plugin, but
// avoids a Gradle-plugin-portal lookup at Docker-build time.) Callers
// should always provide real values when constructing rows manually —
// the defaults are for the JPA bootstrap path only and would fail the
// NOT NULL constraints on save if persisted as-is.
class IngestedMessage(

    @Column(name = "source_protocol", nullable = false)
    @Enumerated(EnumType.STRING)
    @JdbcTypeCode(SqlTypes.NAMED_ENUM)
    var sourceProtocol: IngestedMessageSourceProtocol = IngestedMessageSourceProtocol.OTHER,

    @Column(name = "source_system", nullable = false)
    var sourceSystem: String = "",

    @Column(name = "source_id", nullable = false)
    var sourceId: String = "",

    @Column(name = "message_type", nullable = false)
    var messageType: String = "",

    @Column(name = "raw_message", nullable = false)
    var rawMessage: String = "",

    @Column(name = "raw_content_type", nullable = false)
    var rawContentType: String = "",

    // DB DEFAULT now() handles unset on insert; keep it nullable here so a
    // freshly constructed (unsaved) entity is valid, then JPA will read the
    // server-assigned timestamp back on flush+refresh.
    @Column(name = "received_at", nullable = false, insertable = false, updatable = false)
    var receivedAt: OffsetDateTime? = null,

    @Column(name = "status", nullable = false)
    @Enumerated(EnumType.STRING)
    @JdbcTypeCode(SqlTypes.NAMED_ENUM)
    var status: IngestedMessageStatus = IngestedMessageStatus.RECEIVED,

    @Column(name = "attempt_count", nullable = false)
    var attemptCount: Int = 0,

    @Column(name = "last_attempt_at")
    var lastAttemptAt: OffsetDateTime? = null,

    @Column(name = "next_attempt_at")
    var nextAttemptAt: OffsetDateTime? = null,

    @Column(name = "last_error")
    var lastError: String? = null,

    @Column(name = "delivered_at")
    var deliveredAt: OffsetDateTime? = null,

    /**
     * Correlation id for the message (Epic #387, ticket #388).
     *
     * Server-assigned (UUID v4) by [com.bzonfhir.subscriptionservice
     * .interfaceengine.routes.IngestRoutes] when an MLLP message lands.
     * HL7 v2 has no transport-level correlation header, so every inbound
     * row gets a fresh id at receive time. The same value is then:
     *
     *   - written to MDC for every log line emitted while the row is
     *     being received / persisted / worked,
     *   - sent as the `X-Correlation-Id` header on the matchbox
     *     `$transform` and HAPI Bundle POST,
     *   - exposed by the admin /messages/{id} endpoint so an operator
     *     can paste it into `kubectl logs ... | grep` and pull the full
     *     pipeline trace.
     *
     * Nullable because:
     *
     *   1. Rows persisted before this migration (V003) have no value.
     *      The worker tolerates that by treating "no correlation_id" as
     *      "generate one on first processing".
     *   2. Future inbound channels (FHIR REST, EHR_NATIVE_API) might
     *      legitimately carry an upstream-generated id; if none arrives
     *      we fall back to a server-generated one, but the column itself
     *      stays nullable so a sender that explicitly sends a null
     *      header isn't blocked by a DB NOT NULL constraint.
     */
    @Column(name = "correlation_id")
    var correlationId: String? = null,

    /**
     * W3C `traceparent`-encoded trace context (Epic #387, ticket #394).
     *
     * Captured at MLLP receive time so the async worker can restore the
     * SAME trace context when it picks the row up later — the worker's
     * `worker.process` span ends up as a CHILD of the receive span, not
     * the root of its own trace.
     *
     * Format is the W3C-defined transport string `00-<trace-id>-<span-id>-<flags>`
     * (53 ASCII chars for the v00 header). We store it as plain TEXT so
     * the SDK's TextMapPropagator can parse it back without our app
     * having to know the internal trace-id / span-id binary shape.
     *
     * Nullable for the same reasons [correlationId] is: rows persisted
     * before this migration have no value, and a JVM with
     * `OTEL_SDK_DISABLED=true` writes NULL here (no active span context
     * to encode). The worker tolerates NULL by starting a fresh root
     * span — same behaviour as if OTel wasn't configured at all.
     */
    @Column(name = "trace_context")
    var traceContext: String? = null,

    /**
     * Canonical FHIR references for resources HAPI created when the worker
     * POSTed the transaction Bundle (Epic #387, ticket #392). Populated
     * AFTER the successful HAPI POST by parsing the TRANSACTION_RESPONSE
     * bundle's `entry[].response.location` fields and normalizing each
     * to `ResourceType/id` form (HAPI returns the version-history URL,
     * e.g. `Patient/123/_history/1`, which we trim).
     *
     * Nullable to tolerate:
     *
     *   - Rows persisted before V005 — they have no list; the admin
     *     effects view treats this as `effects_status="unknown"`.
     *   - Rows in non-DELIVERED status — by definition no HAPI POST
     *     succeeded so no refs to list. The list is `null`, NOT an empty
     *     array, because "POSTed and got zero" is a distinguishable case
     *     (rare; would be a transaction Bundle with no `entry[].response`).
     *
     * Mapped as TEXT[] via Hibernate's `SqlTypes.ARRAY`. The kotlin-noarg
     * fallback (default-value-on-every-constructor-parameter) requires a
     * default here too; emptyList() would be wrong because it implies
     * "POSTed, zero refs" — so we default to null and require callers to
     * pass the real list when they have one.
     */
    @Column(name = "created_resource_refs", columnDefinition = "text[]")
    @JdbcTypeCode(SqlTypes.ARRAY)
    var createdResourceRefs: Array<String>? = null,
) {
    @Id
    @GeneratedValue(strategy = GenerationType.IDENTITY)
    @Column(name = "id", nullable = false, updatable = false)
    var id: Long? = null
}

/**
 * Status enum mirroring the Postgres `ingested_message_status` ENUM
 * created by V002. Values + ordering MUST match the SQL; reordering or
 * renaming requires a coordinated Flyway migration to ALTER TYPE.
 */
enum class IngestedMessageStatus {
    RECEIVED,
    TRANSFORMING,
    DELIVERED,
    FAILED,
    DEAD_LETTER,
}

/**
 * Source-protocol enum mirroring `ingested_message_source_protocol`.
 * v1 only writes HL7V2_MLLP; the others are reserved so the schema
 * doesn't need migrating when we add new inbound channels.
 */
enum class IngestedMessageSourceProtocol {
    HL7V2_MLLP,
    FHIR_REST,
    EHR_NATIVE_API,
    OTHER,
}
