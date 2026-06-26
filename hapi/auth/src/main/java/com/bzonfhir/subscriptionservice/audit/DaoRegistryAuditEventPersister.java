package com.bzonfhir.subscriptionservice.audit;

import org.hl7.fhir.r4.model.AuditEvent;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import ca.uhn.fhir.interceptor.model.RequestPartitionId;
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
      //
      // We explicitly pin the partition to DEFAULT here because the partition-identify
      // pointcuts (STORAGE_PARTITION_IDENTIFY_CREATE / _ANY) only fire for *real* request
      // pipelines — a SystemRequestDetails with no partition set produces a HAPI-1319
      // "No interceptor provided a value" exception when partitioning is wired on
      // (which is the default for this image, regardless of whether tenants exist).
      // AuditEvents are always written into the same partition as the resource they
      // audit; for the multi-tenant case ticket #392 will revisit this to copy the
      // caller's tenant partition forward. For now (single-tenant DEFAULT) this is
      // correct and unblocks the entire interceptor.
      SystemRequestDetails systemRequest = new SystemRequestDetails();
      systemRequest.setRequestPartitionId(RequestPartitionId.defaultPartition());
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
