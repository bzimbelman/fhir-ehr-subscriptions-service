package com.bzonfhir.subscriptionservice.validation;

import static org.assertj.core.api.Assertions.assertThat;

import org.junit.jupiter.api.Test;
import org.springframework.boot.autoconfigure.AutoConfigurations;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

import ca.uhn.fhir.context.FhirContext;
import ca.uhn.fhir.rest.server.interceptor.RequestValidatingInterceptor;
import ca.uhn.fhir.validation.IInstanceValidatorModule;
import ca.uhn.fhir.validation.ResultSeverityEnum;

/**
 * Verifies the wiring contract of {@link ProfileValidationAutoConfiguration}:
 *
 * <ul>
 *   <li>{@code mode=OFF} (or unset) — NO {@link RequestValidatingInterceptor} bean is created.
 *   <li>{@code mode=WARN} — interceptor is created, {@code failOnSeverity=null} (no rejection),
 *       and the validation result is folded into the response's OperationOutcome.
 *   <li>{@code mode=ENFORCE} — interceptor is created, {@code failOnSeverity=ERROR} (rejection
 *       on ERROR/FATAL findings), and the result is still folded into the OperationOutcome.
 * </ul>
 *
 * <p>We don't spin up a real {@link ca.uhn.fhir.rest.server.RestfulServer} here — the
 * interceptor registration glue is exercised by the same {@code SmartInitializingSingleton}
 * pattern as the auth layer (already covered by integration tests), and recreating a full
 * HAPI server in unit tests buys nothing. End-to-end validation behavior is verified by the
 * containerized e2e in {@code scripts/e2e-validation.sh}.
 */
class ProfileValidationAutoConfigurationTest {

  private final ApplicationContextRunner runner =
      new ApplicationContextRunner()
          .withConfiguration(AutoConfigurations.of(ProfileValidationAutoConfiguration.class))
          // The auto-configuration needs a FhirContext + an IInstanceValidatorModule in the
          // context — the HAPI starter contributes both at runtime; in tests we supply
          // stand-ins via this @Configuration.
          .withUserConfiguration(TestSupportConfig.class);

  @Test
  void modeOffRegistersNoInterceptor() {
    runner
        .run(
            ctx -> {
              assertThat(ctx).doesNotHaveBean(RequestValidatingInterceptor.class);
            });
  }

  @Test
  void modeUnsetRegistersNoInterceptor() {
    // Re-run with NO property override at all. Default of `OFF` should be applied; no
    // interceptor bean should exist. This guards against accidentally setting
    // matchIfMissing=true on the conditional.
    new ApplicationContextRunner()
        .withConfiguration(AutoConfigurations.of(ProfileValidationAutoConfiguration.class))
        .withUserConfiguration(TestSupportConfig.class)
        .run(
            ctx -> {
              assertThat(ctx).doesNotHaveBean(RequestValidatingInterceptor.class);
            });
  }

  @Test
  void modeWarnRegistersInterceptorThatLogsButAccepts() {
    runner
        .withPropertyValues("subscription-service.validation.mode=warn")
        .run(
            ctx -> {
              assertThat(ctx).hasSingleBean(RequestValidatingInterceptor.class);
              RequestValidatingInterceptor interceptor =
                  ctx.getBean(RequestValidatingInterceptor.class);
              // In WARN mode we MUST NOT reject the request — failOnSeverity stays null
              // (HAPI's default behavior when not set). The validation findings still flow
              // into the OperationOutcome.
              assertThat(getFailOnSeverity(interceptor)).isNull();
              assertThat(interceptor.isAddValidationResultsToResponseOperationOutcome()).isTrue();
            });
  }

  @Test
  void modeEnforceRegistersInterceptorThatFailsOnError() {
    runner
        .withPropertyValues("subscription-service.validation.mode=enforce")
        .run(
            ctx -> {
              assertThat(ctx).hasSingleBean(RequestValidatingInterceptor.class);
              RequestValidatingInterceptor interceptor =
                  ctx.getBean(RequestValidatingInterceptor.class);
              // In ENFORCE mode any ERROR-or-higher finding rejects the request.
              // HAPI translates a failed validation in a request-side interceptor to
              // HTTP 412 Precondition Failed with an OperationOutcome body.
              assertThat(getFailOnSeverity(interceptor)).isEqualTo(ResultSeverityEnum.ERROR);
              assertThat(interceptor.isAddValidationResultsToResponseOperationOutcome()).isTrue();
            });
  }

  @Test
  void modeCaseInsensitive() {
    // Sanity: spring binds the enum case-insensitively. Operators that set
    // SUBSCRIPTION_SERVICE_VALIDATION_MODE=ENFORCE (the conventional env-var shape) get the
    // same wiring as those that use `enforce`.
    runner
        .withPropertyValues("subscription-service.validation.mode=ENFORCE")
        .run(
            ctx -> {
              assertThat(ctx).hasSingleBean(RequestValidatingInterceptor.class);
            });
  }

  // ----- helpers -----

  /**
   * Pulls the {@code myFailOnSeverity} field off the interceptor. There's no public getter on
   * {@code BaseValidatingInterceptor}; reflection is the cheapest way to assert that we
   * configured the interceptor correctly without resorting to a full integration test.
   */
  private static ResultSeverityEnum getFailOnSeverity(RequestValidatingInterceptor interceptor) {
    try {
      java.lang.reflect.Field f =
          interceptor
              .getClass()
              .getSuperclass()
              .getDeclaredField("myFailOnSeverity");
      f.setAccessible(true);
      Integer ord = (Integer) f.get(interceptor);
      return ord == null ? null : ResultSeverityEnum.values()[ord];
    } catch (ReflectiveOperationException e) {
      throw new AssertionError("Could not read myFailOnSeverity via reflection", e);
    }
  }

  /**
   * Minimal Spring config providing the collaborators the auto-configuration expects to find
   * in the HAPI runtime context. The {@link IInstanceValidatorModule} is a no-op stub — we
   * only assert on the configured-but-not-yet-invoked interceptor shape here.
   */
  @Configuration
  static class TestSupportConfig {

    @Bean
    public FhirContext fhirContext() {
      return FhirContext.forR4();
    }

    @Bean
    public IInstanceValidatorModule instanceValidator() {
      // Returning a tiny lambda is enough — the wiring tests never call this.
      return ctx -> {};
    }
  }
}
