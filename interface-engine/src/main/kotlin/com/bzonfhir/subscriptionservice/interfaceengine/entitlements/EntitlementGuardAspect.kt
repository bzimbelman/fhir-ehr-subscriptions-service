package com.bzonfhir.subscriptionservice.interfaceengine.entitlements

import com.bzonfhir.subscriptionservice.interfaceengine.entitlements.config.LicenseProperties
import org.aspectj.lang.ProceedingJoinPoint
import org.aspectj.lang.annotation.Around
import org.aspectj.lang.annotation.Aspect
import org.aspectj.lang.reflect.MethodSignature
import org.slf4j.LoggerFactory

/**
 * Spring AOP guard for [RequiresEntitlement]-annotated methods
 * (ticket #460, Epic #428).
 *
 * On every call to an annotated method:
 *
 *   1. If [LicenseProperties.enabled] is `false`, run the body unconditionally.
 *      (Dev mode escape hatch.)
 *   2. If the [EntitlementHolder]'s active set contains the entitlement, run
 *      the body.
 *   3. Otherwise:
 *      - `failMode=DENY`: throw [EntitlementMissingException]. The method
 *        body never runs.
 *      - `failMode=LOG`: log a WARN with the entitlement string + method
 *        name, then run the body anyway.
 *
 * Using `@Around` (rather than `@Before`) because we want to suppress the
 * method body entirely in the DENY branch — `@Before` would still let the
 * target run after throwing, which Spring AOP would translate as
 * "advice threw, target skipped" anyway, but `@Around` is the conventional
 * shape for "gate a method body".
 *
 * Resolving the annotation off the [MethodSignature] rather than the join
 * point's target avoids issues with proxies — Spring AOP gives us the real
 * declared method here.
 */
@Aspect
class EntitlementGuardAspect(
    private val holder: EntitlementHolder,
    private val properties: LicenseProperties,
) {

    private val log = LoggerFactory.getLogger(EntitlementGuardAspect::class.java)

    @Around("@annotation(com.bzonfhir.subscriptionservice.interfaceengine.entitlements.RequiresEntitlement)")
    fun gate(joinPoint: ProceedingJoinPoint): Any? {
        if (!properties.enabled) {
            // The whole runtime is disabled — every gate is permissive.
            return joinPoint.proceed()
        }

        val signature = joinPoint.signature as MethodSignature
        val annotation = signature.method.getAnnotation(RequiresEntitlement::class.java)
            ?: return joinPoint.proceed()   // belt-and-suspenders: pointcut already matched

        val required = annotation.value
        if (holder.current().has(required)) {
            return joinPoint.proceed()
        }

        when (properties.failMode) {
            LicenseProperties.FailMode.DENY -> {
                log.debug(
                    "blocking call to {} — entitlement \"{}\" not granted",
                    signature.toShortString(), required,
                )
                throw EntitlementMissingException(required)
            }
            LicenseProperties.FailMode.LOG -> {
                log.warn(
                    "entitlement \"{}\" not granted but fail-mode=LOG — letting call to {} proceed",
                    required, signature.toShortString(),
                )
                return joinPoint.proceed()
            }
        }
    }
}
