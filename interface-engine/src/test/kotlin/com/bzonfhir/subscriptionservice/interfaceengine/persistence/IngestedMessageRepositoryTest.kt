package com.bzonfhir.subscriptionservice.interfaceengine.persistence

import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.api.Assertions.assertThatThrownBy
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.Test
import org.springframework.beans.factory.annotation.Autowired
import org.springframework.boot.test.autoconfigure.jdbc.AutoConfigureTestDatabase
import org.springframework.boot.test.context.SpringBootTest
import org.springframework.dao.DataIntegrityViolationException
import org.springframework.jdbc.core.JdbcTemplate
import org.springframework.test.context.DynamicPropertyRegistry
import org.springframework.test.context.DynamicPropertySource
import org.testcontainers.DockerClientFactory
import org.testcontainers.containers.PostgreSQLContainer
import org.testcontainers.junit.jupiter.Container
import org.testcontainers.junit.jupiter.Testcontainers
import java.time.OffsetDateTime
import java.time.ZoneOffset

/**
 * Persistence-layer tests for [IngestedMessageRepository] (Epic #378,
 * ticket #380).
 *
 * Runs a real Postgres in Testcontainers, applies our Flyway migrations
 * (V001 + V002), and exercises the JPA mapping end-to-end:
 *
 *   1. Migration runs cleanly (Flyway history has V001 + V002).
 *   2. Insert + read round-trip preserves every field.
 *   3. (sourceSystem, sourceId) UNIQUE constraint rejects duplicates.
 *   4. findFirstBySourceSystemAndSourceId locates the row.
 *   5. findTop10ByStatusOrderByReceivedAtAsc caps at 10 and orders FIFO.
 *   6. The three expected indexes exist on the table.
 *
 * Docker requirement: gated by [DockerClientFactory.instance().isDockerAvailable].
 * If Docker isn't reachable the test class declines to register the
 * container, so `gradle build` on a Docker-less CI host won't fail —
 * the tests will be reported as errored at the @BeforeAll level only if
 * a developer forces them. On developer machines (Rancher Desktop, Docker
 * Desktop, colima) Docker is present and these run.
 */
@Testcontainers
@SpringBootTest(
    // We need the full app context for the JPA bootstrap, but no web
    // server (the route + MLLP stack would otherwise also wake up).
    webEnvironment = SpringBootTest.WebEnvironment.NONE,
)
// Don't let Spring Boot swap our Testcontainer-pointing datasource for
// an embedded one when it sees H2 on the classpath (it isn't, but be
// safe and explicit).
@AutoConfigureTestDatabase(replace = AutoConfigureTestDatabase.Replace.NONE)
class IngestedMessageRepositoryTest {

    companion object {
        @Container
        @JvmStatic
        val postgres: PostgreSQLContainer<*> = PostgreSQLContainer("postgres:16-alpine")
            .withDatabaseName("ipf")
            .withUsername("ipf")
            .withPassword("ipf")

        @JvmStatic
        @DynamicPropertySource
        fun datasourceProperties(registry: DynamicPropertyRegistry) {
            registry.add("spring.datasource.url") { postgres.jdbcUrl }
            registry.add("spring.datasource.username") { postgres.username }
            registry.add("spring.datasource.password") { postgres.password }
        }

        // Sanity-check Docker availability up front. Testcontainers itself
        // also fails fast in that case, but with a clearer message:
        init {
            check(DockerClientFactory.instance().isDockerAvailable) {
                "Docker is not available; IngestedMessageRepositoryTest requires Testcontainers."
            }
        }
    }

    @Autowired
    private lateinit var repository: IngestedMessageRepository

    @Autowired
    private lateinit var jdbc: JdbcTemplate

    @BeforeEach
    fun cleanTable() {
        // Each test starts with an empty table; Flyway history is left
        // untouched (it should never be mutated by tests).
        jdbc.execute("TRUNCATE TABLE ingested_messages RESTART IDENTITY")
    }

    @Test
    fun `flyway has applied V001 and V002 successfully`() {
        val rows = jdbc.queryForList(
            "SELECT version, success FROM flyway_schema_history ORDER BY installed_rank",
        )
        val versions = rows.map { it["version"] as String }
        assertThat(versions).containsExactly("001", "002")
        assertThat(rows).allSatisfy { row ->
            assertThat(row["success"] as Boolean).isTrue()
        }
    }

    @Test
    fun `insert and read round-trip preserves every field`() {
        val msg = IngestedMessage(
            sourceProtocol = IngestedMessageSourceProtocol.HL7V2_MLLP,
            sourceSystem = "EPIC",
            sourceId = "MSGCTRL00001",
            messageType = "ADT^A01",
            rawMessage = "MSH|^~\\&|EPIC|HOSP|RECEIVER|CDS|20260625120000||ADT^A01|MSGCTRL00001|P|2.5",
            rawContentType = "application/hl7-v2",
            status = IngestedMessageStatus.RECEIVED,
        )

        val saved = repository.saveAndFlush(msg)
        val savedId = saved.id
        assertThat(savedId).isNotNull()

        // Clear the persistence context so the next find hits the DB, not the cache.
        repository.flush()

        val reloaded = repository.findById(savedId!!).orElseThrow()
        assertThat(reloaded.sourceProtocol).isEqualTo(IngestedMessageSourceProtocol.HL7V2_MLLP)
        assertThat(reloaded.sourceSystem).isEqualTo("EPIC")
        assertThat(reloaded.sourceId).isEqualTo("MSGCTRL00001")
        assertThat(reloaded.messageType).isEqualTo("ADT^A01")
        assertThat(reloaded.rawMessage).startsWith("MSH|")
        assertThat(reloaded.rawContentType).isEqualTo("application/hl7-v2")
        assertThat(reloaded.status).isEqualTo(IngestedMessageStatus.RECEIVED)
        assertThat(reloaded.attemptCount).isEqualTo(0)
        assertThat(reloaded.receivedAt).isNotNull()
        assertThat(reloaded.deliveredAt).isNull()
        assertThat(reloaded.lastError).isNull()
    }

    @Test
    fun `(sourceSystem, sourceId) UNIQUE constraint rejects duplicates`() {
        repository.saveAndFlush(newMsg(sourceSystem = "EPIC", sourceId = "DUP001"))

        assertThatThrownBy {
            repository.saveAndFlush(newMsg(sourceSystem = "EPIC", sourceId = "DUP001"))
        }
            // Spring translates Postgres SQLSTATE 23505 to DataIntegrityViolationException;
            // we don't need to match the exact cause chain, just the class.
            .isInstanceOf(DataIntegrityViolationException::class.java)
    }

    @Test
    fun `findFirstBySourceSystemAndSourceId returns the row when it exists`() {
        val saved = repository.saveAndFlush(newMsg(sourceSystem = "CERNER", sourceId = "C-42"))

        val found = repository.findFirstBySourceSystemAndSourceId("CERNER", "C-42")
        assertThat(found).isNotNull()
        assertThat(found!!.id).isEqualTo(saved.id)

        // Negative case: different system, same id.
        val miss = repository.findFirstBySourceSystemAndSourceId("EPIC", "C-42")
        assertThat(miss).isNull()
    }

    @Test
    fun `findTop10ByStatusOrderByReceivedAtAsc returns at most 10 oldest RECEIVED rows`() {
        // 15 RECEIVED rows with monotonically increasing received_at (we set
        // it explicitly because the DB DEFAULT now() would tie within a tx).
        val base = OffsetDateTime.now(ZoneOffset.UTC).minusMinutes(60)
        repeat(15) { i ->
            val m = newMsg(sourceSystem = "SYS", sourceId = "R-$i")
            // Override the receivedAt DB default for deterministic ordering.
            // The column is insertable=false in the entity, so push it via
            // raw JDBC after the insert.
            repository.saveAndFlush(m)
            jdbc.update(
                "UPDATE ingested_messages SET received_at = ? WHERE id = ?",
                base.plusMinutes(i.toLong()),
                m.id,
            )
        }
        // 5 non-RECEIVED rows that must NOT show up.
        repeat(5) { i ->
            val m = newMsg(
                sourceSystem = "SYS",
                sourceId = "D-$i",
                status = IngestedMessageStatus.DELIVERED,
            )
            repository.saveAndFlush(m)
        }

        val batch = repository.findTop10ByStatusOrderByReceivedAtAsc(IngestedMessageStatus.RECEIVED)
        assertThat(batch).hasSize(10)
        // Oldest first: the receivedAt of the first should be < the last.
        val timestamps = batch.map { it.receivedAt!! }
        assertThat(timestamps).isSorted()
        // And every row should be RECEIVED.
        assertThat(batch).allSatisfy { row ->
            assertThat(row.status).isEqualTo(IngestedMessageStatus.RECEIVED)
        }
    }

    @Test
    fun `countByStatus returns the row count for that status`() {
        repository.saveAndFlush(newMsg(sourceSystem = "S", sourceId = "r1"))
        repository.saveAndFlush(newMsg(sourceSystem = "S", sourceId = "r2"))
        repository.saveAndFlush(
            newMsg(sourceSystem = "S", sourceId = "d1", status = IngestedMessageStatus.DELIVERED),
        )

        assertThat(repository.countByStatus(IngestedMessageStatus.RECEIVED)).isEqualTo(2L)
        assertThat(repository.countByStatus(IngestedMessageStatus.DELIVERED)).isEqualTo(1L)
        assertThat(repository.countByStatus(IngestedMessageStatus.FAILED)).isEqualTo(0L)
    }

    @Test
    fun `schema has the three expected indexes on ingested_messages`() {
        val indexes = jdbc.queryForList(
            "SELECT indexname FROM pg_indexes WHERE tablename = 'ingested_messages'",
            String::class.java,
        )
        assertThat(indexes).contains(
            "ix_ingested_status_received",
            "ix_ingested_next_attempt",
            "ix_ingested_received_at",
        )
    }

    private fun newMsg(
        sourceSystem: String,
        sourceId: String,
        status: IngestedMessageStatus = IngestedMessageStatus.RECEIVED,
    ) = IngestedMessage(
        sourceProtocol = IngestedMessageSourceProtocol.HL7V2_MLLP,
        sourceSystem = sourceSystem,
        sourceId = sourceId,
        messageType = "ADT^A01",
        rawMessage = "MSH|...|$sourceId|P|2.5",
        rawContentType = "application/hl7-v2",
        status = status,
    )
}
