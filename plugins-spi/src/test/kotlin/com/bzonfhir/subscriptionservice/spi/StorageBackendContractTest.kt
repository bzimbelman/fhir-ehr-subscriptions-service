package com.bzonfhir.subscriptionservice.spi

import com.bzonfhir.subscriptionservice.spi.meta.CreateOutcome
import com.bzonfhir.subscriptionservice.spi.meta.FhirResource
import com.bzonfhir.subscriptionservice.spi.meta.PluginMeta
import com.bzonfhir.subscriptionservice.spi.meta.PluginSupplier
import com.bzonfhir.subscriptionservice.spi.meta.SearchCriteria
import com.bzonfhir.subscriptionservice.spi.meta.SearchResult
import com.bzonfhir.subscriptionservice.spi.meta.UpdateOutcome
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

class StorageBackendContractTest {

    @Test
    fun `StorageBackend shape compiles and round-trips create read search update`() {
        // In-memory test stub. Real backends (HAPI JPA, Elasticsearch, ...) live
        // in their own modules; this proves the SPI shape supports the basic
        // four operations.
        val store = mutableMapOf<String, FhirResource>()

        val backend: StorageBackend = object : StorageBackend {
            override val meta = PluginMeta(
                id = "test-inmemory",
                version = "0.0.1",
                schemaVersion = 1,
                supplier = PluginSupplier.COMMUNITY,
                description = "Test stub in-memory storage",
            )

            override fun create(resource: FhirResource): CreateOutcome {
                val id = resource.id ?: "auto-${store.size + 1}"
                val key = "${resource.type}/$id"
                store[key] = resource.copy(id = id)
                return CreateOutcome(id = id, versionId = "1")
            }

            override fun read(type: String, id: String): FhirResource? = store["$type/$id"]

            override fun search(criteria: SearchCriteria): SearchResult {
                val matches = store.values.filter { it.type == criteria.resourceType }
                return SearchResult(total = matches.size, resources = matches)
            }

            override fun update(resource: FhirResource): UpdateOutcome {
                val id = requireNotNull(resource.id) { "update requires id" }
                val key = "${resource.type}/$id"
                val existed = store.containsKey(key)
                store[key] = resource
                return UpdateOutcome(id = id, versionId = "2", created = !existed)
            }
        }

        val createOutcome = backend.create(
            FhirResource(type = "Patient", id = "p1", json = """{"resourceType":"Patient","id":"p1"}"""),
        )
        assertThat(createOutcome.id).isEqualTo("p1")
        assertThat(createOutcome.versionId).isEqualTo("1")

        val read = backend.read("Patient", "p1")
        assertThat(read).isNotNull
        assertThat(read!!.json).contains("Patient")

        val search = backend.search(SearchCriteria(resourceType = "Patient"))
        assertThat(search.total).isEqualTo(1)
        assertThat(search.resources).hasSize(1)

        val updateOutcome = backend.update(
            FhirResource(type = "Patient", id = "p1", json = """{"resourceType":"Patient","id":"p1","active":true}"""),
        )
        assertThat(updateOutcome.created).isFalse()
        assertThat(updateOutcome.versionId).isEqualTo("2")

        val upsertOutcome = backend.update(
            FhirResource(type = "Patient", id = "p2", json = """{"resourceType":"Patient","id":"p2"}"""),
        )
        assertThat(upsertOutcome.created).isTrue()
    }
}
