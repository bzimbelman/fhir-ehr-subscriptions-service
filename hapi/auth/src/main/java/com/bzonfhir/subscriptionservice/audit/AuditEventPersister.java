package com.bzonfhir.subscriptionservice.audit;

import org.hl7.fhir.r4.model.AuditEvent;

/**
 * Strategy for persisting an {@link AuditEvent} produced by
 * {@link AuditEventInterceptor}. Pulled into its own interface so the interceptor's
 * unit tests can capture AuditEvents in-memory without spinning up HAPI's JPA stack.
 *
 * <p>The production implementation is {@link DaoRegistryAuditEventPersister}, which
 * delegates to HAPI's {@code DaoRegistry} to write the resource through the same
 * persistence path any other FHIR Create takes.
 *
 * <p>Implementations MUST be safe to call from the request thread (the interceptor
 * fires synchronously after the operation completes). Any failure inside the persister
 * is the implementation's responsibility to log and swallow — audit writes must never
 * leak an exception back into the request pipeline that would convert a successful
 * 2xx into a 5xx because audit-write failed. The whole point of the audit trail is to
 * record what already happened, not to gate it.
 */
@FunctionalInterface
public interface AuditEventPersister {

  /**
   * Persist a freshly-built {@link AuditEvent}. Implementations swallow failures
   * (logging them) — see class javadoc.
   */
  void persist(AuditEvent event);
}
