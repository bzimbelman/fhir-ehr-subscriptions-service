package com.bzonfhir.subscriptionservice.interfaceengine.observability

import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.AfterEach
import org.junit.jupiter.api.Test
import org.slf4j.MDC

/**
 * Unit-level coverage for [CorrelationId] (Epic #387, ticket #388).
 *
 * Focused on the small policy decisions that aren't exercised by the
 * integration tests (which only assert on the happy path):
 *
 *   - UUID generation produces unique values
 *   - sanitizeOrGenerate accepts well-formed values
 *   - sanitizeOrGenerate rejects control characters / overlong values
 *   - withMdc cleans up on success AND on exception
 */
class CorrelationIdTest {

    @AfterEach
    fun cleanup() {
        MDC.clear()
    }

    @Test
    fun `generate produces unique values`() {
        val ids = (1..100).map { CorrelationId.generate() }.toSet()
        assertThat(ids).hasSize(100)
    }

    @Test
    fun `sanitizeOrGenerate passes through valid id`() {
        val ok = "abc-DEF_012.345"
        assertThat(CorrelationId.sanitizeOrGenerate(ok)).isEqualTo(ok)
    }

    @Test
    fun `sanitizeOrGenerate generates fresh id for null`() {
        assertThat(CorrelationId.sanitizeOrGenerate(null)).matches("[0-9a-f-]{36}")
    }

    @Test
    fun `sanitizeOrGenerate generates fresh id for blank`() {
        assertThat(CorrelationId.sanitizeOrGenerate("   ")).matches("[0-9a-f-]{36}")
    }

    @Test
    fun `sanitizeOrGenerate rejects control character`() {
        val bad = "abc\ninjected"
        val result = CorrelationId.sanitizeOrGenerate(bad)
        assertThat(result).isNotEqualTo(bad)
        assertThat(result).matches("[0-9a-f-]{36}")
    }

    @Test
    fun `sanitizeOrGenerate rejects overlong value`() {
        val tooLong = "a".repeat(CorrelationId.MAX_HEADER_LENGTH + 1)
        val result = CorrelationId.sanitizeOrGenerate(tooLong)
        assertThat(result).isNotEqualTo(tooLong)
        assertThat(result.length).isLessThan(CorrelationId.MAX_HEADER_LENGTH)
    }

    @Test
    fun `withMdc sets and clears the value on success`() {
        assertThat(MDC.get(CorrelationId.MDC_KEY)).isNull()
        val result = CorrelationId.withMdc("xyz") {
            assertThat(MDC.get(CorrelationId.MDC_KEY)).isEqualTo("xyz")
            "ok"
        }
        assertThat(result).isEqualTo("ok")
        assertThat(MDC.get(CorrelationId.MDC_KEY)).isNull()
    }

    @Test
    fun `withMdc clears the value on exception`() {
        assertThat(MDC.get(CorrelationId.MDC_KEY)).isNull()
        try {
            CorrelationId.withMdc("zzz") {
                throw IllegalStateException("kaboom")
            }
        } catch (_: IllegalStateException) {
            // expected
        }
        assertThat(MDC.get(CorrelationId.MDC_KEY)).isNull()
    }

    @Test
    fun `withMdc restores prior value on exit`() {
        MDC.put(CorrelationId.MDC_KEY, "outer")
        CorrelationId.withMdc("inner") {
            assertThat(MDC.get(CorrelationId.MDC_KEY)).isEqualTo("inner")
        }
        assertThat(MDC.get(CorrelationId.MDC_KEY)).isEqualTo("outer")
    }
}
