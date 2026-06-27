package com.bzonfhir.subscriptionservice.plugins.profileloader

import java.util.concurrent.ConcurrentHashMap

/**
 * In-memory catalog of profiles successfully loaded at boot.
 *
 * Other parts of the runtime query the registry to:
 *
 *  - List loaded profiles in the operator UI's "what's loaded" footer.
 *  - Resolve a profile by id when an ingest plugin needs the matching
 *    mapping/quirks block for a message (`MSH-3 = epic` -> find `epic`
 *    profile).
 *  - Surface profile metadata in `bd remember`-style listings.
 *
 * Thread safety: backed by a [ConcurrentHashMap] because the
 * [ProfileLoader] populates the registry asynchronously on
 * `ApplicationReadyEvent` and reads can race with the populate phase
 * in slow-boot scenarios. Reads after `load()` completes are
 * lock-free.
 *
 * Why a plain Spring `@Component` rather than a Spring bean factory:
 * the registry's lifecycle matches the application context exactly
 * (one instance per host, lives for the host's lifetime) and the
 * loader needs a stable reference to mutate.
 */
class ProfileRegistry {

    private val byId: MutableMap<String, ProfileManifest> = ConcurrentHashMap()

    /**
     * Register a parsed manifest. If a profile with the same id is
     * already registered, the new one replaces it — this happens when
     * an operator drops in a newer version of a profile YAML and
     * restarts. Returns the manifest that was previously registered
     * under [manifest]`.profile.id`, or `null` when this is the first.
     */
    fun register(manifest: ProfileManifest): ProfileManifest? {
        return byId.put(manifest.profile.id, manifest)
    }

    /**
     * Look a profile up by [id]. Returns `null` when no profile with
     * that id was loaded. Callers MUST handle the null case rather
     * than throwing — a missing profile is a deployment-config issue,
     * not a programming error.
     */
    fun findById(id: String): ProfileManifest? = byId[id]

    /**
     * Snapshot of all loaded profiles. The returned list is a copy;
     * mutations don't propagate back into the registry.
     */
    fun findAll(): List<ProfileManifest> = byId.values.toList()

    /**
     * How many profiles are currently registered. Useful in tests and
     * in the operator UI's footer.
     */
    fun count(): Int = byId.size

    /**
     * Remove all registered profiles. Used by tests; not exposed to
     * production callers (a fresh process gives the same effect).
     */
    internal fun clear() = byId.clear()
}
