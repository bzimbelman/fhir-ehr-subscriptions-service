package com.bzonfhir.subscriptionservice.spi

import com.bzonfhir.subscriptionservice.spi.meta.CreateOutcome
import com.bzonfhir.subscriptionservice.spi.meta.FhirResource
import com.bzonfhir.subscriptionservice.spi.meta.PluginMeta
import com.bzonfhir.subscriptionservice.spi.meta.SearchCriteria
import com.bzonfhir.subscriptionservice.spi.meta.SearchResult
import com.bzonfhir.subscriptionservice.spi.meta.UpdateOutcome

/**
 * SPI #6 — Pluggable FHIR storage backend.
 *
 * The FOSS image's default storage is HAPI's JPA store, running against
 * Postgres. That's the right answer for the median deployment. It is
 * NOT the right answer for:
 *
 *  - Search-heavy read workloads where Elasticsearch's inverted index
 *    out-performs JPA (`StorageBackend` impl: `elasticsearch-fhir`).
 *  - Very-large deployments needing horizontal sharding
 *    (`StorageBackend` impl: `aurora-multi-region` or similar).
 *  - Air-gapped environments where Postgres is forbidden and an
 *    in-memory or file-based store is required.
 *
 * A [StorageBackend] plugin replaces the HAPI JPA layer wholesale. The
 * runtime stops invoking HAPI's DAO registry and routes every
 * create / read / search / update through this interface instead.
 *
 * # Bootstrapping
 *
 * Exactly ONE [StorageBackend] is active per deployment. If multiple
 * plugins claim the surface the runtime fails fast at boot with a list
 * of conflicting plugin ids; the operator picks one via configuration.
 *
 * # FHIR semantics
 *
 * Implementations are expected to preserve FHIR R4 semantics:
 *
 *  - Logical id assignment (`Patient/123`), separate from technical id.
 *  - Versioning (`Patient/123/_history/4`) — the [CreateOutcome] and
 *    [UpdateOutcome] versionIds let the runtime construct
 *    version-specific links.
 *  - Conditional update (if-match against version).
 *  - Standard search params + result paging.
 *
 * The SPI does NOT cover transactions / bundles. The runtime breaks
 * bundles into individual create/update calls before invoking the
 * backend; a backend can choose to atomically commit a sequence by
 * inspecting its own thread-local state if it wants.
 *
 * # Stability: EXPERIMENTAL
 *
 * This surface is the most likely to evolve in the early life of the
 * project. We expect to add a `delete` operation and a `patch`
 * operation in v0.2, plus a streaming `search` for very-large result
 * sets. Plugins that depend on this should follow the SPI changelog
 * closely.
 */
interface StorageBackend {

    /**
     * Identity.
     */
    val meta: PluginMeta

    /**
     * Create a new resource. Returns the assigned logical id +
     * version.
     */
    fun create(resource: FhirResource): CreateOutcome

    /**
     * Read by logical id. Returns `null` if the resource doesn't
     * exist (the runtime translates that to a 404 for the caller).
     */
    fun read(type: String, id: String): FhirResource?

    /**
     * Run a search. The runtime expects standard FHIR search-param
     * semantics; backend may push the search down to its underlying
     * index or evaluate in-process.
     */
    fun search(criteria: SearchCriteria): SearchResult

    /**
     * Update or upsert. [UpdateOutcome.created] tells the runtime
     * whether this turned into an insert (operator may have allowed
     * client-specified ids via `?_upsert=true`-style configuration).
     */
    fun update(resource: FhirResource): UpdateOutcome
}
