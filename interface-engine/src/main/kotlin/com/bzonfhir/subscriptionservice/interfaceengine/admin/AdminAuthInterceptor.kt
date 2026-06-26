package com.bzonfhir.subscriptionservice.interfaceengine.admin

import jakarta.servlet.http.HttpServletRequest
import jakarta.servlet.http.HttpServletResponse
import org.slf4j.LoggerFactory
import org.springframework.beans.factory.annotation.Value
import org.springframework.context.annotation.Configuration
import org.springframework.stereotype.Component
import org.springframework.web.servlet.HandlerInterceptor
import org.springframework.web.servlet.config.annotation.InterceptorRegistry
import org.springframework.web.servlet.config.annotation.WebMvcConfigurer
import jakarta.annotation.PostConstruct

/**
 * Single-token bearer auth gate for `/admin/` + glob endpoints (Epic #378, ticket #384).
 *
 * Auth model:
 *   - `IPF_ADMIN_AUTH_TOKEN` env var is unset / empty → endpoints are OPEN.
 *     A WARN is logged at startup so an operator who left it off in dev
 *     knows the situation. This is the documented dev-convenience default.
 *   - `IPF_ADMIN_AUTH_TOKEN` is set → every `/admin/` + glob request MUST send
 *     `Authorization: Bearer <token>` matching exactly. Otherwise 401.
 *
 * Why not Spring Security: that pulls in a much larger surface (filter
 * chain, CSRF, session mgmt, principal handling) than a single-token
 * check warrants. A HandlerInterceptor wired through WebMvcConfigurer
 * matches the scope.
 */
@Component
class AdminAuthInterceptor(
    @Value("\${ipf.admin.auth-token:}") private val configuredToken: String,
) : HandlerInterceptor {

    private val log = LoggerFactory.getLogger(AdminAuthInterceptor::class.java)

    @PostConstruct
    fun warnIfOpen() {
        if (configuredToken.isBlank()) {
            log.warn(
                "Admin endpoints under /admin/** are UNAUTHENTICATED " +
                    "(IPF_ADMIN_AUTH_TOKEN is unset or empty). " +
                    "Set IPF_ADMIN_AUTH_TOKEN to require Bearer auth.",
            )
        } else {
            log.info("Admin endpoints under /admin/** require Bearer auth.")
        }
    }

    override fun preHandle(
        request: HttpServletRequest,
        response: HttpServletResponse,
        handler: Any,
    ): Boolean {
        if (configuredToken.isBlank()) {
            // Auth off — let everything through.
            return true
        }
        val header = request.getHeader("Authorization")
        if (header == null || !header.startsWith("Bearer ")) {
            return reject(response, "missing Bearer token")
        }
        val presented = header.removePrefix("Bearer ").trim()
        // Constant-time-ish compare. Both sides are operator-controlled
        // ASCII tokens; we still avoid early-exit string compare to be a
        // good neighbor under timing observation.
        if (!constantTimeEquals(presented, configuredToken)) {
            return reject(response, "invalid Bearer token")
        }
        return true
    }

    private fun reject(response: HttpServletResponse, reason: String): Boolean {
        response.status = HttpServletResponse.SC_UNAUTHORIZED
        response.setHeader("WWW-Authenticate", "Bearer")
        response.contentType = "application/json"
        response.writer.write("""{"error":"unauthorized","message":"$reason"}""")
        return false
    }

    private fun constantTimeEquals(a: String, b: String): Boolean {
        val aBytes = a.toByteArray(Charsets.UTF_8)
        val bBytes = b.toByteArray(Charsets.UTF_8)
        if (aBytes.size != bBytes.size) return false
        var diff = 0
        for (i in aBytes.indices) {
            diff = diff or (aBytes[i].toInt() xor bBytes[i].toInt())
        }
        return diff == 0
    }
}

/**
 * Mounts [AdminAuthInterceptor] on the `/admin/` glob path. The
 * interceptor itself stays a plain @Component (easier to test in
 * isolation) and the MVC wiring lives in this @Configuration.
 */
@Configuration
class AdminWebMvcConfig(
    private val adminAuthInterceptor: AdminAuthInterceptor,
) : WebMvcConfigurer {
    override fun addInterceptors(registry: InterceptorRegistry) {
        registry.addInterceptor(adminAuthInterceptor).addPathPatterns("/admin/**")
    }
}
