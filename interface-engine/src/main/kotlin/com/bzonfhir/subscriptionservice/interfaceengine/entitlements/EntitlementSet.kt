package com.bzonfhir.subscriptionservice.interfaceengine.entitlements

/**
 * The set of entitlement strings the customer's license grants
 * (ticket #460, Epic #428).
 *
 * Mirrors the shape of the UI's `EntitlementSet` (ui/src/lib/license/types.ts)
 * so the backend's runtime gate and the UI's route gate ask exactly the same
 * question: `entitlements.has("audit.export.iti20")`.
 *
 * IMPORTANT: the JVM has its own copy of the entitlement set. We do NOT take
 * entitlements off an incoming HTTP request header — see EntitlementHolder
 * for the authoritative-set lifecycle. This value type is what the holder
 * surfaces and what the @RequiresEntitlement guard consults.
 *
 * Empty set == FOSS mode == no commercial entitlements granted. All
 * @RequiresEntitlement-annotated methods fail open in `log` fail-mode and
 * closed in the default `deny` fail-mode.
 */
data class EntitlementSet(private val entries: Set<String>) {

    /** True iff [entitlement] is present in the active set. Case-sensitive, exact match. */
    fun has(entitlement: String): Boolean = entries.contains(entitlement)

    /** Stable view as a sorted list — used for log lines + diagnostics endpoints. */
    fun toSortedList(): List<String> = entries.sorted()

    /** Size of the set — useful for log lines + tests. */
    val size: Int get() = entries.size

    /** True iff no entitlements are present (FOSS / dev mode). */
    fun isEmpty(): Boolean = entries.isEmpty()

    companion object {
        /** The empty set — FOSS / dev mode. */
        val EMPTY: EntitlementSet = EntitlementSet(emptySet())

        /** Build from an arbitrary collection of strings (deduplicates). */
        fun of(entitlements: Collection<String>): EntitlementSet =
            if (entitlements.isEmpty()) EMPTY else EntitlementSet(entitlements.toSet())
    }
}
