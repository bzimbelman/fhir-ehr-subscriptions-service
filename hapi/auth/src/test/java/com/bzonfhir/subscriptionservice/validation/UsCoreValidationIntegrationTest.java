package com.bzonfhir.subscriptionservice.validation;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.assertj.core.api.Assumptions.assumeThat;

import java.io.FileInputStream;
import java.io.IOException;
import java.io.InputStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.util.List;

import org.hl7.fhir.common.hapi.validation.support.CommonCodeSystemsTerminologyService;
import org.hl7.fhir.common.hapi.validation.support.InMemoryTerminologyServerValidationSupport;
import org.hl7.fhir.common.hapi.validation.support.NpmPackageValidationSupport;
import org.hl7.fhir.common.hapi.validation.support.PrePopulatedValidationSupport;
import org.hl7.fhir.common.hapi.validation.support.SnapshotGeneratingValidationSupport;
import org.hl7.fhir.common.hapi.validation.support.ValidationSupportChain;
import org.hl7.fhir.common.hapi.validation.validator.FhirInstanceValidator;
import org.hl7.fhir.instance.model.api.IBaseResource;
import org.hl7.fhir.utilities.npm.NpmPackage;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.mockito.Mockito;

import ca.uhn.fhir.context.FhirContext;
import ca.uhn.fhir.context.support.DefaultProfileValidationSupport;
import ca.uhn.fhir.parser.IParser;
import ca.uhn.fhir.rest.api.RestOperationTypeEnum;
import ca.uhn.fhir.rest.api.server.RequestDetails;
import ca.uhn.fhir.rest.server.exceptions.UnprocessableEntityException;
import ca.uhn.fhir.rest.server.interceptor.RequestValidatingInterceptor;
import ca.uhn.fhir.validation.FhirValidator;
import ca.uhn.fhir.validation.IInstanceValidatorModule;
import ca.uhn.fhir.validation.ResultSeverityEnum;
import ca.uhn.fhir.validation.ValidationResult;

/**
 * Integration-style test that exercises {@link RequestValidatingInterceptor} the way the
 * deployed HAPI server does — with a real {@link FhirInstanceValidator} loaded with the
 * US Core 7.0 IG that ships in {@code hapi/igs/}.
 *
 * <p>These tests assert the contract operators care about:
 *
 * <ul>
 *   <li>A US Core-conforming Patient produces NO ERROR-severity findings (happy path).
 *   <li>A Patient missing the required US Core identifier slice produces an ERROR-severity
 *       finding.
 *   <li>In WARN mode (no {@code failOnSeverity}) the interceptor lets the request through but
 *       stashes the validation result on the request, so HAPI's
 *       {@code ValidationResultEnrichingInterceptor} machinery can fold it into the response
 *       OperationOutcome.
 *   <li>In ENFORCE mode ({@code failOnSeverity=ERROR}) the interceptor throws
 *       {@link UnprocessableEntityException} — HAPI translates that to HTTP 422 +
 *       OperationOutcome on the wire (FHIR's standard "resource failed validation"
 *       response code; HAPI hard-codes this in {@code BaseValidatingInterceptor.fail}).
 * </ul>
 *
 * <p>Prerequisite: the US Core tgz must be present at {@code ../igs/hl7.fhir.us.core-7.0.0.tgz}
 * (one level up from the auth module, in {@code hapi/igs/}). The repo's
 * {@code scripts/fetch-igs.sh} populates it; if absent these tests are skipped with a clear
 * pointer rather than failing — the auto-configuration unit tests still cover the wiring
 * contract.
 */
@TestInstance(TestInstance.Lifecycle.PER_CLASS)
class UsCoreValidationIntegrationTest {

  // hapi/auth -> hapi -> hapi/igs/...
  private static final Path US_CORE_TGZ =
      Paths.get("..").resolve("igs").resolve("hl7.fhir.us.core-7.0.0.tgz").toAbsolutePath();

  private FhirContext ctx;
  private IInstanceValidatorModule instanceValidator;
  private IParser jsonParser;

  @BeforeAll
  void loadValidator() throws IOException {
    assumeThat(Files.exists(US_CORE_TGZ))
        .as(
            "US Core IG tarball missing at %s — run scripts/fetch-igs.sh from the repo root "
                + "to populate it. The auto-configuration wiring tests still cover the "
                + "contract; this test only adds runtime-behavior coverage.",
            US_CORE_TGZ)
        .isTrue();

    ctx = FhirContext.forR4();
    jsonParser = ctx.newJsonParser();

    // NpmPackageValidationSupport's only public loader is `loadPackageFromClasspath`, but
    // we have a file path (the IG tarballs are bind-mounted from the host's hapi/igs/ dir
    // in production; for tests we read the same file directly). The underlying
    // PrePopulatedValidationSupport accepts loaded resources via addResource(), so we
    // open the tgz with NpmPackage.fromPackage() and feed each conformance resource in.
    NpmPackageValidationSupport npmSupport = new NpmPackageValidationSupport(ctx);
    try (InputStream in = new FileInputStream(US_CORE_TGZ.toFile())) {
      NpmPackage pkg = NpmPackage.fromPackage(in);
      loadConformanceResources(ctx, pkg, npmSupport);
    }

    ValidationSupportChain chain =
        new ValidationSupportChain(
            npmSupport,
            new DefaultProfileValidationSupport(ctx),
            new CommonCodeSystemsTerminologyService(ctx),
            new InMemoryTerminologyServerValidationSupport(ctx),
            new SnapshotGeneratingValidationSupport(ctx));

    instanceValidator = new FhirInstanceValidator(chain);
  }

  @Test
  void conformingPatientProducesNoErrors() {
    IBaseResource patient = parse(USCORE_CONFORMING_PATIENT_JSON);

    FhirValidator validator = ctx.newValidator().registerValidatorModule(instanceValidator);
    ValidationResult result = validator.validateWithResult(patient);

    boolean hasError =
        result.getMessages().stream().anyMatch(m -> m.getSeverity() == ResultSeverityEnum.ERROR
            || m.getSeverity() == ResultSeverityEnum.FATAL);
    assertThat(hasError)
        .as(
            "Conforming Patient should not produce ERROR-severity findings; got: %s",
            result.getMessages())
        .isFalse();
  }

  @Test
  void nonConformingPatientProducesErrorSeverity() {
    IBaseResource patient = parse(NON_CONFORMING_PATIENT_JSON);

    FhirValidator validator = ctx.newValidator().registerValidatorModule(instanceValidator);
    ValidationResult result = validator.validateWithResult(patient);

    // A Patient claiming the US Core profile but missing the required identifier slice (and
    // other required US Core fields) must surface at least one ERROR. This is the canonical
    // "fails US Core validation" shape the e2e script POSTs.
    boolean hasError =
        result.getMessages().stream().anyMatch(m -> m.getSeverity() == ResultSeverityEnum.ERROR
            || m.getSeverity() == ResultSeverityEnum.FATAL);
    assertThat(hasError)
        .as(
            "Non-conforming Patient (claiming us-core-patient profile but missing required "
                + "identifier/name slices) must produce at least one ERROR-severity finding. "
                + "Findings: %s",
            result.getMessages())
        .isTrue();
  }

  @Test
  void warnModeInterceptorAcceptsNonConformingResource() {
    // Build an interceptor exactly the way ProfileValidationAutoConfiguration does for WARN
    // mode (failOnSeverity=null) and exercise it through its public validateRequest path.
    RequestValidatingInterceptor interceptor = buildInterceptorForTest();
    interceptor.setAddValidationResultsToResponseOperationOutcome(true);
    interceptor.setFailOnSeverity(null);

    // BaseValidatingInterceptor exposes a protected validate(T, RequestDetails) method that
    // runs the validator and applies the configured pass/fail policy. We invoke it via the
    // package-visible doValidate to avoid the full HAPI server harness.
    ValidationResult result = validateThroughInterceptor(interceptor, NON_CONFORMING_PATIENT_JSON);

    // The validation result itself still carries the findings — what differs between WARN
    // and ENFORCE is whether the interceptor's `fail(...)` path is taken. In WARN mode the
    // result is produced but no exception is thrown (no call to fail()).
    assertThat(result.getMessages())
        .as("WARN mode still computes the full validation result.")
        .isNotEmpty();
  }

  @Test
  void enforceModeInterceptorRejectsNonConformingResource() {
    RequestValidatingInterceptor interceptor = buildInterceptorForTest();
    interceptor.setAddValidationResultsToResponseOperationOutcome(true);
    interceptor.setFailOnSeverity(ResultSeverityEnum.ERROR);

    // In ENFORCE mode the interceptor's post-validate path calls fail() on any ERROR-severity
    // finding, which throws UnprocessableEntityException -> HTTP 422 on the wire (the
    // FHIR-standard "resource failed validation" response; HAPI hard-codes this in
    // BaseValidatingInterceptor.fail). We surface that exception directly here.
    assertThatThrownBy(() -> validateThroughInterceptor(interceptor, NON_CONFORMING_PATIENT_JSON))
        .isInstanceOf(UnprocessableEntityException.class);
  }

  @Test
  void enforceModeAcceptsConformingResource() {
    RequestValidatingInterceptor interceptor = buildInterceptorForTest();
    interceptor.setAddValidationResultsToResponseOperationOutcome(true);
    interceptor.setFailOnSeverity(ResultSeverityEnum.ERROR);

    // Happy path — a conforming Patient must NOT trip the ENFORCE interceptor.
    ValidationResult result = validateThroughInterceptor(interceptor, USCORE_CONFORMING_PATIENT_JSON);
    boolean hasError =
        result.getMessages().stream().anyMatch(m -> m.getSeverity() == ResultSeverityEnum.ERROR
            || m.getSeverity() == ResultSeverityEnum.FATAL);
    assertThat(hasError)
        .as(
            "ENFORCE mode happy path: conforming Patient must produce no ERROR findings. "
                + "Got: %s",
            result.getMessages())
        .isFalse();
  }

  // ---------------- helpers ----------------

  /**
   * Builds an interceptor pre-loaded with a {@link FhirValidator} carrying our IG-aware
   * {@code instanceValidator}. {@link BaseValidatingInterceptor#validate(Object, RequestDetails)}
   * falls back to {@code requestDetails.getServer().getFhirContext().newValidator()} when the
   * interceptor's own validator is null — and our mocked RequestDetails has no server. Setting
   * the validator up-front avoids that fallback path entirely.
   */
  private RequestValidatingInterceptor buildInterceptorForTest() {
    RequestValidatingInterceptor interceptor = new RequestValidatingInterceptor();
    // Pre-build the FhirValidator and hand it to the interceptor — addValidatorModule(...)
    // and setValidator(...) are mutually exclusive in BaseValidatingInterceptor.
    FhirValidator validator = ctx.newValidator().registerValidatorModule(instanceValidator);
    interceptor.setValidator(validator);
    return interceptor;
  }

  /**
   * Invokes the interceptor's validate-and-policy path the same way HAPI's REST layer does
   * for an incoming POST/PUT. Returns the {@link ValidationResult} on success; throws
   * {@link PreconditionFailedException} (i.e. HTTP 412) when {@code failOnSeverity} trips.
   */
  private ValidationResult validateThroughInterceptor(
      RequestValidatingInterceptor interceptor, String resourceJson) {
    // BaseValidatingInterceptor.validate(T, RequestDetails) is protected; we use reflection
    // to invoke it. Cleaner than wiring up a full RestfulServer for a unit test and matches
    // how HAPI's own RequestValidatingInterceptor tests exercise the class. The interceptor
    // bails out (returns null) when RequestDetails is null OR when the operation type is
    // unset, so we mock a CREATE request — that's the shape an inbound POST /Patient takes.
    RequestDetails requestDetails = Mockito.mock(RequestDetails.class);
    Mockito.when(requestDetails.getRestOperationType()).thenReturn(RestOperationTypeEnum.CREATE);
    try {
      java.lang.reflect.Method m =
          interceptor
              .getClass()
              .getSuperclass()
              .getDeclaredMethod(
                  "validate", Object.class, ca.uhn.fhir.rest.api.server.RequestDetails.class);
      m.setAccessible(true);
      return (ValidationResult) m.invoke(interceptor, resourceJson, requestDetails);
    } catch (java.lang.reflect.InvocationTargetException e) {
      // Unwrap so the AssertJ thrown-by assertion sees the real cause.
      if (e.getCause() instanceof RuntimeException re) {
        throw re;
      }
      throw new RuntimeException(e.getCause());
    } catch (ReflectiveOperationException e) {
      throw new AssertionError(
          "Couldn't invoke BaseValidatingInterceptor.validate(...) via reflection. The "
              + "HAPI ABI changed; update the test to match.",
          e);
    }
  }

  /**
   * Reads every {@code package/*.json} StructureDefinition / ValueSet / CodeSystem /
   * SearchParameter from the NPM package and registers it on the support module.
   *
   * <p>We hand-roll this because {@link NpmPackageValidationSupport#loadPackageFromClasspath}
   * is the only public loader and we have a file path, not a classpath resource. The work
   * is small — list folders, parse, register.
   */
  private static void loadConformanceResources(
      FhirContext ctx, NpmPackage pkg, PrePopulatedValidationSupport target) throws IOException {
    IParser parser = ctx.newJsonParser();
    // Conformance-bearing folder names per the NPM IG layout convention.
    for (String folder : List.of("package")) {
      List<String> files;
      try {
        files = pkg.list(folder);
      } catch (IOException e) {
        continue;
      }
      for (String fname : files) {
        if (!fname.endsWith(".json")) {
          continue;
        }
        try (InputStream in = pkg.load(folder, fname)) {
          try {
            IBaseResource res = parser.parseResource(in);
            target.addResource(res);
          } catch (Exception parseFailure) {
            // Some package files (e.g. ig-r4.json metadata) aren't FHIR resources. Skip.
          }
        }
      }
    }
  }

  private IBaseResource parse(String json) {
    return jsonParser.parseResource(json);
  }

  // A Patient that conforms to us-core-patient r4 7.0.0:
  // - declares the profile
  // - has at least one identifier (system + value)
  // - has a name with a family
  // - has gender
  private static final String USCORE_CONFORMING_PATIENT_JSON =
      "{\n"
          + "  \"resourceType\": \"Patient\",\n"
          + "  \"meta\": {\n"
          + "    \"profile\": [\"http://hl7.org/fhir/us/core/StructureDefinition/us-core-patient\"]\n"
          + "  },\n"
          + "  \"identifier\": [{\n"
          + "    \"system\": \"http://hospital.example.org/mrn\",\n"
          + "    \"value\": \"MRN-12345\"\n"
          + "  }],\n"
          + "  \"name\": [{\"family\": \"Test\", \"given\": [\"Jane\"]}],\n"
          + "  \"gender\": \"female\",\n"
          + "  \"birthDate\": \"1980-01-01\"\n"
          + "}";

  // A Patient that CLAIMS to conform to us-core-patient but is missing required slices:
  // - no identifier (US Core requires 1..*)
  // - no gender
  // - name has no family
  // This is the e2e script's canonical "fails US Core validation" Patient.
  private static final String NON_CONFORMING_PATIENT_JSON =
      "{\n"
          + "  \"resourceType\": \"Patient\",\n"
          + "  \"meta\": {\n"
          + "    \"profile\": [\"http://hl7.org/fhir/us/core/StructureDefinition/us-core-patient\"]\n"
          + "  },\n"
          + "  \"name\": [{\"given\": [\"NoFamily\"]}]\n"
          + "}";

}
