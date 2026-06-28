package com.bzonfhir.subscriptionservice.interfaceengine.entitlements

import org.springframework.http.HttpStatus
import org.springframework.http.ResponseEntity
import org.springframework.web.bind.annotation.ExceptionHandler
import org.springframework.web.bind.annotation.RestControllerAdvice

/**
 * Translates [EntitlementMissingException] into HTTP 402 Payment Required
 * for any controller that surfaces a commercial-only endpoint
 * (ticket #460, Epic #428).
 *
 * 402 is the "right" status for "feature exists, your license doesn't pay
 * for it" — distinct from 403 (which implies an authorization decision the
 * server's identity model can change without a sales call). The
 * specification status for 402 is "experimental" but in practice it's the
 * canonical paywall code; Stripe and others use it the same way.
 *
 * The body intentionally surfaces the entitlement string so the caller's
 * error message can render the customer-facing wording "this requires the
 * `audit.export.iti20` entitlement" without the client needing to map an
 * opaque error code.
 */
@RestControllerAdvice
class EntitlementMissingControllerAdvice {

    @ExceptionHandler(EntitlementMissingException::class)
    fun handle(ex: EntitlementMissingException): ResponseEntity<Map<String, String>> =
        ResponseEntity.status(HttpStatus.PAYMENT_REQUIRED).body(
            mapOf(
                "error" to "entitlement_missing",
                "entitlement" to ex.entitlement,
                "message" to (ex.message ?: "missing required entitlement"),
            )
        )
}
