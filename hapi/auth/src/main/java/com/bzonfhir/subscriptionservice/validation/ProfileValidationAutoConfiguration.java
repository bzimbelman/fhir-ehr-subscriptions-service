package com.bzonfhir.subscriptionservice.validation;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.SmartInitializingSingleton;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.autoconfigure.AutoConfiguration;
import org.springframework.boot.autoconfigure.condition.ConditionalOnBean;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.boot.context.properties.EnableConfigurationProperties;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Conditional;
import org.springframework.context.annotation.ConfigurationCondition;
import org.springframework.core.type.AnnotatedTypeMetadata;

import ca.uhn.fhir.rest.server.RestfulServer;
import ca.uhn.fhir.rest.server.interceptor.RequestValidatingInterceptor;
import ca.uhn.fhir.validation.IInstanceValidatorModule;
import ca.uhn.fhir.validation.ResultSeverityEnum;

/**
 * Spring Boot auto-configuration that wires HAPI's
 * {@link RequestValidatingInterceptor} into the running HAPI Spring context when
 * {@code SUBSCRIPTION_SERVICE_VALIDATION_MODE=warn} or {@code =enforce}.
 *
 * <p>Discovered via
 * {@code META-INF/spring/org.springframework.boot.autoconfigure.AutoConfiguration.imports}
 * — when this JAR is on the HAPI classpath, Spring Boot's autoconfig machinery picks it up.
 *
 * <p>The whole configuration is gated by a {@link ValidationModeEnabledCondition} that checks
 * for {@code mode=warn|enforce}. When {@code mode=off} (the default) Spring skips this
 * autoconfig entirely and HAPI behaves like the upstream image — no validation interceptor,
 * no overhead.
 *
 * <h2>Validation support</h2>
 *
 * <p>The {@link RequestValidatingInterceptor} delegates to whatever {@link IInstanceValidatorModule}
 * is registered. The HAPI JPA starter exposes one as a Spring bean (via
 * {@code ca.uhn.fhir.jpa.config.ValidationSupportConfig.instanceValidator(...)}); we inject it
 * here. That validator already knows about every IG installed at boot through
 * {@code hapi.fhir.implementationguides} — so the validator picks up US Core 7.0 +
 * Subscriptions Backport R4 automatically without us re-loading the tgz files.
 *
 * <h2>Modes</h2>
 *
 * <ul>
 *   <li><b>WARN</b> — {@code failOnSeverity=null}: the interceptor never rejects the request;
 *       findings are folded into the response's OperationOutcome via
 *       {@code addValidationResultsToResponseOperationOutcome=true}. Subscribers get a 2xx
 *       response but can see what was wrong.
 *   <li><b>ENFORCE</b> — {@code failOnSeverity=ERROR}: any ERROR or FATAL finding rejects
 *       the request. HAPI's interceptor implementation throws
 *       {@code UnprocessableEntityException}, which the REST layer translates into HTTP 422
 *       Unprocessable Entity with an OperationOutcome body listing the issues. (HTTP 422 is
 *       the FHIR-standard response code for failed resource validation; HAPI hard-codes it
 *       in {@code BaseValidatingInterceptor.fail}.)
 * </ul>
 *
 * <p>See {@code docs/architecture.md} "Profile validation (US Core)" for the design rationale.
 *
 * <p>Ticket: #367.
 */
@AutoConfiguration
@EnableConfigurationProperties(ValidationProperties.class)
@Conditional(ProfileValidationAutoConfiguration.ValidationModeEnabledCondition.class)
public class ProfileValidationAutoConfiguration {

  private static final Logger log = LoggerFactory.getLogger(ProfileValidationAutoConfiguration.class);

  /**
   * Bean predicate: register only when the resolved mode is {@code WARN} or {@code ENFORCE}.
   *
   * <p>We can't use a single {@link ConditionalOnProperty} with {@code havingValue}
   * because the standard form only matches one value. Two conditions OR'd together
   * (one for {@code warn}, one for {@code enforce}) would mean writing the same logic twice.
   * A single {@link ConfigurationCondition} is cleaner and lets the existing
   * {@link ValidationProperties.ValidationMode} enum stay the source of truth.
   */
  public static class ValidationModeEnabledCondition implements ConfigurationCondition {

    @Override
    public ConfigurationPhase getConfigurationPhase() {
      return ConfigurationPhase.PARSE_CONFIGURATION;
    }

    @Override
    public boolean matches(
        org.springframework.context.annotation.ConditionContext context,
        AnnotatedTypeMetadata metadata) {
      String raw =
          context.getEnvironment().getProperty("subscription-service.validation.mode");
      if (raw == null) {
        return false;
      }
      try {
        ValidationProperties.ValidationMode mode =
            ValidationProperties.ValidationMode.valueOf(raw.trim().toUpperCase());
        return mode == ValidationProperties.ValidationMode.WARN
            || mode == ValidationProperties.ValidationMode.ENFORCE;
      } catch (IllegalArgumentException e) {
        // Unknown value (e.g., "yes", "true"): conservatively treat as OFF. This matches
        // the behavior of `subscription-service.auth.enabled=garbage` in the auth layer.
        return false;
      }
    }
  }

  /**
   * The validating interceptor itself. Configured once at bean creation time per the resolved
   * mode; the {@code RequestValidatingInterceptor} is thread-safe so a singleton bean is
   * appropriate.
   *
   * <p>The {@link IInstanceValidatorModule} is the one contributed by the HAPI JPA starter —
   * it carries the loaded IGs. {@link ConditionalOnBean} guards against the rare case where
   * we're packaged into something other than the JPA starter (e.g., a HAPI plain server
   * without the JPA module loaded); in that case no interceptor is registered and a clear
   * log line explains why.
   */
  @Bean
  @ConditionalOnBean(IInstanceValidatorModule.class)
  public RequestValidatingInterceptor profileValidatingInterceptor(
      ValidationProperties props, IInstanceValidatorModule instanceValidator) {
    RequestValidatingInterceptor interceptor = new RequestValidatingInterceptor();
    interceptor.addValidatorModule(instanceValidator);
    // Always surface validation findings on the response OperationOutcome. In WARN mode this
    // is the operator-visible signal that something is non-conformant; in ENFORCE mode it
    // doubles as the rejection-reason payload.
    interceptor.setAddValidationResultsToResponseOperationOutcome(true);

    if (props.getMode() == ValidationProperties.ValidationMode.ENFORCE) {
      interceptor.setFailOnSeverity(ResultSeverityEnum.ERROR);
      log.info(
          "Subscription-service profile validation: ENFORCE — non-conforming bundles will "
              + "be rejected with HTTP 422 + OperationOutcome.");
    } else {
      // mode=WARN — explicit null means "never fail". The default would be ERROR; we override.
      interceptor.setFailOnSeverity(null);
      log.info(
          "Subscription-service profile validation: WARN — non-conforming bundles will be "
              + "accepted; findings surfaced in response OperationOutcome.");
    }
    return interceptor;
  }

  /**
   * Registers the interceptor on the HAPI {@link RestfulServer} once it's fully constructed.
   * Mirrors the registration pattern from {@code AuthAutoConfiguration} — the HAPI starter's
   * {@code restfulServer} bean does NOT auto-pick-up arbitrary interceptor beans from the
   * context unless they're listed under {@code hapi.fhir.custom-interceptor-classes}, so we
   * register explicitly via a {@link SmartInitializingSingleton} hook.
   *
   * <p>This bean is also gated by {@link ConditionalOnBean} on the interceptor so that a
   * misconfigured deployment (no {@link IInstanceValidatorModule}) doesn't try to register
   * a null interceptor.
   */
  @Bean
  @ConditionalOnBean(RequestValidatingInterceptor.class)
  public SmartInitializingSingleton subscriptionServiceProfileValidatingInterceptorRegistrar(
      @Autowired(required = false) RestfulServer restfulServer,
      RequestValidatingInterceptor interceptor) {
    return () -> {
      if (restfulServer == null) {
        log.warn(
            "No RestfulServer bean found — subscription-service profile-validation "
                + "interceptor NOT registered. Fine for unit tests; indicates a packaging "
                + "problem if it happens inside the HAPI image.");
        return;
      }
      restfulServer.registerInterceptor(interceptor);
      log.info(
          "Registered subscription-service RequestValidatingInterceptor on HAPI RestfulServer.");
    };
  }
}
