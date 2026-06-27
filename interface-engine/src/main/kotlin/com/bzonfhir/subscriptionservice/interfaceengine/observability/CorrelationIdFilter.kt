package com.bzonfhir.subscriptionservice.interfaceengine.observability

import jakarta.servlet.FilterChain
import jakarta.servlet.http.HttpServletRequest
import jakarta.servlet.http.HttpServletResponse
import org.slf4j.LoggerFactory
import org.slf4j.MDC
import org.springframework.core.Ordered
import org.springframework.core.annotation.Order
import org.springframework.stereotype.Component
import org.springframework.web.filter.OncePerRequestFilter

/**
 * Servlet filter that establishes a `correlation_id` for every inbound HTTP
 * request to the interface-engine (Epic #387, ticket #388).
 *
 * Behaviour:
 *
 *   1. Read `X-Correlation-Id` from the request headers.
 *   2. If missing or malformed → generate a fresh UUID.
 *   3. Put the value into the SLF4J MDC under `correlation_id` for the
 *      duration of the request.
 *   4. Echo the value back on the response headers so the caller can
 *      correlate their side with our server logs.
 *   5. ALWAYS clear the MDC in a finally block — leaking the value to the
 *      next request handled by the same Tomcat worker thread would
 *      misattribute every subsequent log line.
 *
 * Ordered with [Ordered.HIGHEST_PRECEDENCE] so this filter runs before any
 * other (security, logging, the admin auth interceptor) — the MDC value
 * must be set before ANY log line is emitted for the request, including the
 * "incoming request" line Spring's logging or the auth interceptor writes.
 *
 * Mounted on every URL pattern (the default for OncePerRequestFilter). The
 * filter is cheap (one MDC.put + one header write) so applying it
 * everywhere is fine; the admin auth interceptor still gates the actual
 * authorization on `/admin/` paths.
 */
@Component
@Order(Ordered.HIGHEST_PRECEDENCE)
class CorrelationIdFilter : OncePerRequestFilter() {

    private val log = LoggerFactory.getLogger(CorrelationIdFilter::class.java)

    override fun doFilterInternal(
        request: HttpServletRequest,
        response: HttpServletResponse,
        filterChain: FilterChain,
    ) {
        val inbound = request.getHeader(CorrelationId.HEADER)
        val correlationId = CorrelationId.sanitizeOrGenerate(inbound)

        // Echo on the response so the caller can grep its logs by the
        // same id we used. Set it BEFORE filterChain.doFilter() — once
        // the response is committed the header can't be added.
        response.setHeader(CorrelationId.HEADER, correlationId)

        // Carry the value through the request lifecycle on the MDC. The
        // try/finally guarantees we don't leak it to the next request on
        // this Tomcat worker thread — without it, a thread that handles
        // a request, then handles a different one with no inbound header,
        // would still log the previous request's id from this thread's
        // ThreadLocal MDC.
        MDC.put(CorrelationId.MDC_KEY, correlationId)
        try {
            if (log.isDebugEnabled) {
                log.debug(
                    "request entered method={} path={} correlationIdSource={}",
                    request.method,
                    request.requestURI,
                    if (inbound.isNullOrBlank()) "generated" else "inbound",
                )
            }
            filterChain.doFilter(request, response)
        } finally {
            MDC.remove(CorrelationId.MDC_KEY)
        }
    }
}
