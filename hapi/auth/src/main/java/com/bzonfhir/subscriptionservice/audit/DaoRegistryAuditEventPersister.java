package com.bzonfhir.subscriptionservice.audit;

import org.hl7.fhir.r4.model.AuditEvent;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import ca.uhn.fhir.jpa.api.dao.DaoRegistry;
import ca.uhn.fhir.jpa.api.dao.IFhirResourceDao;
import ca.uhn.fhir.rest.api.server.SystemRequestDetails;

/**
 * Production {@link AuditEventPersister} that writes AuditEvents through HAPI's
 * {@link DaoRegistry}. Uses a {@link SystemRequestDetails} so the create operation
 * bypasses the {@link com.bzonfhir.subscriptionservice.auth.ScopeAuthorizationInterceptor}
 * — otherwise an external caller without {@code system/AuditEvent.c} scope would
 * also be unable to GENERATE their own audit row, which is nonsense.
 *
 * <p>All failures are caught and logged at WARN. Audit writes MUST NOT propagate
 * an exception back into the request pipeline: the whole point of the audit trail
 * is to record what already happened, not to gate it.
 */
public class DaoRegistryAuditEventPersister implements AuditEventPersister {

  private static final Logger log =
      LoggerFactory.getLogger(DaoRegistryAuditEventPersister.class);

  private final DaoRegistry daoRegistry;

  public DaoRegistryAuditEventPersister(DaoRegistry daoRegistry) {
    this.daoRegistry = daoRegistry;
  }

  @Override
  public void persist(AuditEvent event) {
    try {
      IFhirResourceDao<AuditEvent> dao = daoRegistry.getResourceDao(AuditEvent.class);
      // SystemRequestDetails marks this as an internal/server-side request; HAPI's
      // authorization interceptor short-circuits its rule evaluation for these.
      SystemRequestDetails systemRequest = new SystemRequestDetails();
      dao.create(event, systemRequest);
    } catch (Exception e) {
      // Swallow & log. See class javadoc — audit-write failure must NEVER cause the
      // caller's request to fail.
      log.warn(
          "Failed to persist AuditEvent (action={}, outcome={}): {}",
          event.hasAction() ? event.getAction().toCode() : null,
          event.hasOutcome() ? event.getOutcome().toCode() : null,
          e.toString());
    }
  }
}
