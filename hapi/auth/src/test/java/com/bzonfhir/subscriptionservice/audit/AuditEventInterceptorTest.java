package com.bzonfhir.subscriptionservice.audit;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.lenient;
import static org.mockito.Mockito.mock;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

import org.hl7.fhir.r4.model.AuditEvent;
import org.hl7.fhir.r4.model.AuditEvent.AuditEventAction;
import org.hl7.fhir.r4.model.AuditEvent.AuditEventOutcome;
import org.hl7.fhir.r4.model.IdType;
import org.hl7.fhir.r4.model.Patient;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import ca.uhn.fhir.rest.api.RestOperationTypeEnum;
import ca.uhn.fhir.rest.api.server.RequestDetails;
import ca.uhn.fhir.rest.api.server.ResponseDetails;
import ca.uhn.fhir.rest.server.exceptions.AuthenticationException;
import ca.uhn.fhir.rest.server.exceptions.ForbiddenOperationException;
import com.bzonfhir.subscriptionservice.auth.OidcJwtAuthenticationInterceptor;
import com.nimbusds.jwt.JWTClaimsSet;

/**
 * Behavioural tests for {@link AuditEventInterceptor} (ticket #391, Epic #387).
 *
 * <p>Rather than spin up a real HAPI server, we exercise the interceptor's hook entry
 * points ({@link AuditEventInterceptor#afterOutgoingResponse} and
 * {@link AuditEventInterceptor#afterException}) directly with mocked
 * {@link RequestDetails} + a stub {@link AuditEventPersister} that captures the emitted
 * AuditEvents in-memory. Same shape as the existing observability and multitenancy
 * interceptor tests in this module.
 *
 * <p>What we verify, mapped to the ticket-#391 acceptance criteria:
 *
 * <ul>
 *   <li>POST Patient → action=C, agent.altId=&lt;sub&gt;, entity.what=Patient/&lt;id&gt;, outcome=0.
 *   <li>GET Patient/&lt;id&gt; with capture-reads=true → action=R is emitted.
 *   <li>GET Patient/&lt;id&gt; with capture-reads=false (the default) → NO AuditEvent.
 *   <li>DELETE / PUT produce action=D / action=U respectively.
 *   <li>Auth failure (401 — {@link AuthenticationException}) → outcome=8, anonymous agent.
 *   <li>Auth failure (403 — {@link ForbiddenOperationException}) → outcome=4.
 *   <li>Skip-list: {@code GET /fhir/metadata} and {@code GET /fhir/AuditEvent} produce no
 *       AuditEvent (the latter prevents infinite recursion).
 *   <li>Master toggle: {@code subscription-service.audit.enabled=false} → no AuditEvents
 *       even for writes.
 * </ul>
 */
class AuditEventInterceptorTest {

  /** Captures AuditEvents emitted by the interceptor so the test can assert on them. */
  private static final class CapturingPersister implements AuditEventPersister {
    final List<AuditEvent> events = new ArrayList<>();

    @Override
    public void persist(AuditEvent event) {
      events.add(event);
    }
  }

  private AuditProperties props;
  private CapturingPersister persister;
  private AuditEventInterceptor interceptor;

  @BeforeEach
  void setUp() {
    props = new AuditProperties(); // defaults: enabled=true, capture-reads=false, capture-search=false
    persister = new CapturingPersister();
    interceptor = new AuditEventInterceptor(props, persister);
  }

  /**
   * Build a RequestDetails mock with the supplied request path, REST op, resource type,
   * resource id (may be null), and optional JWT claims stashed in userData. Matches the
   * shape produced by {@link OidcJwtAuthenticationInterceptor} at runtime.
   */
  private RequestDetails mockRequest(
      String path,
      RestOperationTypeEnum op,
      String resourceName,
      String id,
      JWTClaimsSet claims) {
    RequestDetails rd = mock(RequestDetails.class);
    lenient().when(rd.getRequestPath()).thenReturn(path);
    lenient().when(rd.getRestOperationType()).thenReturn(op);
    lenient().when(rd.getResourceName()).thenReturn(resourceName);
    lenient().when(rd.getFhirServerBase()).thenReturn("http://test-server/fhir");
    if (id != null && resourceName != null) {
      lenient().when(rd.getId()).thenReturn(new IdType(resourceName, id));
    }
    Map<Object, Object> userData = new HashMap<>();
    if (claims != null) {
      userData.put(OidcJwtAuthenticationInterceptor.USER_DATA_CLAIMS_KEY, claims);
    }
    lenient().when(rd.getUserData()).thenReturn(userData);
    return rd;
  }

  private static JWTClaimsSet claimsFor(String sub, String azp) {
    return new JWTClaimsSet.Builder()
        .subject(sub)
        .claim("azp", azp)
        .claim("preferred_username", sub)
        .build();
  }

  private static ResponseDetails okResponse(int code, org.hl7.fhir.instance.model.api.IBaseResource res) {
    ResponseDetails rd = new ResponseDetails();
    rd.setResponseCode(code);
    if (res != null) {
      rd.setResponseResource(res);
    }
    return rd;
  }

  // ---------------------------- writes ----------------------------

  @Test
  void createPatient_producesAuditEventActionC() {
    RequestDetails rd =
        mockRequest("Patient", RestOperationTypeEnum.CREATE, "Patient", null, claimsFor("svc-acc", "smart-app"));

    Patient created = new Patient();
    created.setId("Patient/123");
    ResponseDetails resp = okResponse(201, created);

    interceptor.afterOutgoingResponse(rd, resp);

    assertThat(persister.events).hasSize(1);
    AuditEvent ev = persister.events.get(0);
    assertThat(ev.getAction()).isEqualTo(AuditEventAction.C);
    assertThat(ev.getOutcome()).isEqualTo(AuditEventOutcome._0);
    // type DICOM rest
    assertThat(ev.getType().getCode()).isEqualTo("rest");
    // agent: user (svc-acc) AND client app (smart-app)
    assertThat(ev.getAgent()).hasSize(2);
    assertThat(ev.getAgent().get(0).getAltId()).isEqualTo("svc-acc");
    assertThat(ev.getAgent().get(0).getRequestor()).isTrue();
    assertThat(ev.getAgent().get(1).getAltId()).isEqualTo("smart-app");
    // entity.what = Patient/123
    assertThat(ev.getEntity()).hasSize(1);
    assertThat(ev.getEntity().get(0).getWhat().getReference()).isEqualTo("Patient/123");
    // period populated (start/end both present)
    assertThat(ev.getPeriod().hasStart()).isTrue();
    assertThat(ev.getPeriod().hasEnd()).isTrue();
    // recorded populated
    assertThat(ev.getRecorded()).isNotNull();
    // source.site = the FHIR base URL
    assertThat(ev.getSource().getSite()).isEqualTo("http://test-server/fhir");
  }

  @Test
  void deletePatient_producesAuditEventActionD() {
    RequestDetails rd =
        mockRequest("Patient/42", RestOperationTypeEnum.DELETE, "Patient", "42", claimsFor("user-1", null));
    ResponseDetails resp = okResponse(204, null);

    interceptor.afterOutgoingResponse(rd, resp);

    assertThat(persister.events).hasSize(1);
    AuditEvent ev = persister.events.get(0);
    assertThat(ev.getAction()).isEqualTo(AuditEventAction.D);
    assertThat(ev.getOutcome()).isEqualTo(AuditEventOutcome._0);
    assertThat(ev.getEntity().get(0).getWhat().getReference()).isEqualTo("Patient/42");
  }

  @Test
  void updatePatient_producesAuditEventActionU() {
    RequestDetails rd =
        mockRequest("Patient/42", RestOperationTypeEnum.UPDATE, "Patient", "42", claimsFor("user-2", null));
    Patient updated = new Patient();
    updated.setId("Patient/42");
    ResponseDetails resp = okResponse(200, updated);

    interceptor.afterOutgoingResponse(rd, resp);

    assertThat(persister.events).hasSize(1);
    AuditEvent ev = persister.events.get(0);
    assertThat(ev.getAction()).isEqualTo(AuditEventAction.U);
    assertThat(ev.getEntity().get(0).getWhat().getReference()).isEqualTo("Patient/42");
  }

  // ---------------------------- reads gated by capture-reads ----------------------------

  @Test
  void readPatient_withCaptureReadsTrue_producesAuditEventActionR() {
    props.setCaptureReads(true);

    RequestDetails rd =
        mockRequest("Patient/42", RestOperationTypeEnum.READ, "Patient", "42", claimsFor("user-3", null));
    Patient read = new Patient();
    read.setId("Patient/42");
    ResponseDetails resp = okResponse(200, read);

    interceptor.afterOutgoingResponse(rd, resp);

    assertThat(persister.events).hasSize(1);
    AuditEvent ev = persister.events.get(0);
    assertThat(ev.getAction()).isEqualTo(AuditEventAction.R);
  }

  @Test
  void readPatient_withCaptureReadsFalse_producesNoAuditEvent() {
    // default capture-reads=false
    RequestDetails rd =
        mockRequest("Patient/42", RestOperationTypeEnum.READ, "Patient", "42", claimsFor("user-3", null));
    Patient read = new Patient();
    read.setId("Patient/42");
    ResponseDetails resp = okResponse(200, read);

    interceptor.afterOutgoingResponse(rd, resp);

    assertThat(persister.events).isEmpty();
  }

  // ---------------------------- skip-list ----------------------------

  @Test
  void getMetadata_producesNoAuditEvent() {
    RequestDetails rd = mockRequest("metadata", RestOperationTypeEnum.METADATA, null, null, null);
    ResponseDetails resp = okResponse(200, null);

    interceptor.afterOutgoingResponse(rd, resp);

    assertThat(persister.events).isEmpty();
  }

  @Test
  void getAuditEvent_producesNoAuditEvent_preventsRecursion() {
    // GET /fhir/AuditEvent — the recursive read MUST NOT produce its own AuditEvent.
    props.setCaptureReads(true);
    props.setCaptureSearch(true);
    RequestDetails rd =
        mockRequest("AuditEvent", RestOperationTypeEnum.SEARCH_TYPE, "AuditEvent", null, claimsFor("admin", null));
    ResponseDetails resp = okResponse(200, null);

    interceptor.afterOutgoingResponse(rd, resp);

    assertThat(persister.events).isEmpty();
  }

  @Test
  void getAuditEventInstance_producesNoAuditEvent() {
    props.setCaptureReads(true);
    RequestDetails rd =
        mockRequest("AuditEvent/123", RestOperationTypeEnum.READ, "AuditEvent", "123", claimsFor("admin", null));
    ResponseDetails resp = okResponse(200, null);

    interceptor.afterOutgoingResponse(rd, resp);

    assertThat(persister.events).isEmpty();
  }

  // ---------------------------- master toggle ----------------------------

  @Test
  void disabled_producesNoAuditEventEvenOnWrite() {
    props.setEnabled(false);
    RequestDetails rd =
        mockRequest("Patient", RestOperationTypeEnum.CREATE, "Patient", null, claimsFor("user-4", null));
    Patient created = new Patient();
    created.setId("Patient/9");
    ResponseDetails resp = okResponse(201, created);

    interceptor.afterOutgoingResponse(rd, resp);

    assertThat(persister.events).isEmpty();
  }

  // ---------------------------- auth failures ----------------------------

  @Test
  void authenticationFailure401_producesOutcome8_andAnonymousAgent() {
    // Unauthenticated request — no claims stashed; the failure path still emits an
    // AuditEvent with the anonymous-agent placeholder so the trail records the
    // attempted access.
    RequestDetails rd = mockRequest("Patient", RestOperationTypeEnum.CREATE, "Patient", null, null);
    AuthenticationException ex =
        new AuthenticationException("Authorization header required");

    interceptor.afterException(rd, ex);

    assertThat(persister.events).hasSize(1);
    AuditEvent ev = persister.events.get(0);
    assertThat(ev.getOutcome()).isEqualTo(AuditEventOutcome._8);
    assertThat(ev.getAgent()).hasSize(1);
    assertThat(ev.getAgent().get(0).getAltId())
        .isEqualTo(AuditEventInterceptor.ANONYMOUS_AGENT);
  }

  @Test
  void forbidden403_producesOutcome4() {
    // Authenticated but the SMART scope didn't cover this operation — minor failure (4).
    RequestDetails rd =
        mockRequest("Patient/5", RestOperationTypeEnum.READ, "Patient", "5", claimsFor("user-5", null));
    props.setCaptureReads(true); // ensure success path WOULD audit; failure path always audits.
    ForbiddenOperationException ex =
        new ForbiddenOperationException("Missing scope system/Patient.r");

    interceptor.afterException(rd, ex);

    assertThat(persister.events).hasSize(1);
    AuditEvent ev = persister.events.get(0);
    assertThat(ev.getOutcome()).isEqualTo(AuditEventOutcome._4);
    // Agent identity preserved on a 403 (we know who tried).
    assertThat(ev.getAgent()).hasSize(1);
    assertThat(ev.getAgent().get(0).getAltId()).isEqualTo("user-5");
  }

  @Test
  void skipList_failurePathDoesNotEmitForMetadata() {
    // Belt-and-braces: even on the failure hook, a metadata-path exception doesn't
    // create an AuditEvent. The skip-list applies to both success and failure.
    RequestDetails rd = mockRequest("metadata", RestOperationTypeEnum.METADATA, null, null, null);
    AuthenticationException ex = new AuthenticationException("nope");

    interceptor.afterException(rd, ex);

    assertThat(persister.events).isEmpty();
  }

  // ---------------------------- builder small-surface ----------------------------

  @Test
  void actionFor_mapsRestOperationCorrectly() {
    assertThat(AuditEventInterceptor.actionFor(RestOperationTypeEnum.CREATE))
        .isEqualTo(AuditEventAction.C);
    assertThat(AuditEventInterceptor.actionFor(RestOperationTypeEnum.READ))
        .isEqualTo(AuditEventAction.R);
    assertThat(AuditEventInterceptor.actionFor(RestOperationTypeEnum.UPDATE))
        .isEqualTo(AuditEventAction.U);
    assertThat(AuditEventInterceptor.actionFor(RestOperationTypeEnum.PATCH))
        .isEqualTo(AuditEventAction.U);
    assertThat(AuditEventInterceptor.actionFor(RestOperationTypeEnum.DELETE))
        .isEqualTo(AuditEventAction.D);
    assertThat(AuditEventInterceptor.actionFor(RestOperationTypeEnum.SEARCH_TYPE))
        .isEqualTo(AuditEventAction.R);
    assertThat(AuditEventInterceptor.actionFor(RestOperationTypeEnum.TRANSACTION))
        .isEqualTo(AuditEventAction.E);
  }
}
