package com.bzonfhir.subscriptionservice.auth;

import static org.assertj.core.api.Assertions.assertThat;

import org.junit.jupiter.api.Test;
import org.springframework.boot.autoconfigure.AutoConfigurations;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;

/**
 * Tests the auto-configuration's startup-time validation. The key behaviour exercised here
 * is the fail-fast guarantee added for ticket #370: when auth is enabled but no issuer is
 * configured, the Spring context must fail to start so the container exits non-zero
 * instead of silently falling back to a baked-in default that points at someone else's
 * Keycloak.
 */
class AuthAutoConfigurationTest {

  private static final String EXPECTED_MESSAGE =
      "subscription-service.auth.issuer is required when auth is enabled. "
          + "Set SUBSCRIPTION_SERVICE_AUTH_ISSUER "
          + "(e.g., https://your-keycloak.example.com/realms/subscription-service) "
          + "or set SUBSCRIPTION_SERVICE_AUTH_ENABLED=false for local dev.";

  private final ApplicationContextRunner contextRunner =
      new ApplicationContextRunner()
          .withConfiguration(AutoConfigurations.of(AuthAutoConfiguration.class));

  @Test
  void failsFast_whenAuthEnabledAndIssuerMissing() {
    contextRunner
        .withPropertyValues("subscription-service.auth.enabled=true")
        // No issuer property set; AuthProperties.issuer defaults to null.
        .run(
            context -> {
              assertThat(context).hasFailed();
              assertThat(context.getStartupFailure())
                  .rootCause()
                  .isInstanceOf(IllegalStateException.class)
                  .hasMessage(EXPECTED_MESSAGE);
            });
  }

  @Test
  void failsFast_whenAuthEnabledAndIssuerBlank() {
    contextRunner
        .withPropertyValues(
            "subscription-service.auth.enabled=true", "subscription-service.auth.issuer=")
        .run(
            context -> {
              assertThat(context).hasFailed();
              assertThat(context.getStartupFailure())
                  .rootCause()
                  .isInstanceOf(IllegalStateException.class)
                  .hasMessage(EXPECTED_MESSAGE);
            });
  }

  @Test
  void failsFast_whenAuthEnabledAndIssuerWhitespace() {
    contextRunner
        .withPropertyValues(
            "subscription-service.auth.enabled=true", "subscription-service.auth.issuer=   ")
        .run(
            context -> {
              assertThat(context).hasFailed();
              assertThat(context.getStartupFailure())
                  .rootCause()
                  .isInstanceOf(IllegalStateException.class)
                  .hasMessage(EXPECTED_MESSAGE);
            });
  }

  @Test
  void doesNotRequireIssuer_whenAuthDisabled() {
    contextRunner
        .withPropertyValues("subscription-service.auth.enabled=false")
        .run(
            context -> {
              // ConditionalOnProperty short-circuits the whole config when disabled, so
              // no validation should occur and no JwtValidator bean should exist.
              assertThat(context).hasNotFailed();
              assertThat(context).doesNotHaveBean(JwtValidator.class);
              assertThat(context).doesNotHaveBean(AuthProperties.class);
            });
  }

  @Test
  void contextLoads_whenIssuerProvided() {
    contextRunner
        .withPropertyValues(
            "subscription-service.auth.enabled=true",
            "subscription-service.auth.issuer=https://issuer.example.com/realms/test")
        .run(
            context -> {
              assertThat(context).hasNotFailed();
              assertThat(context).hasSingleBean(JwtValidator.class);
              assertThat(context).hasSingleBean(AuthProperties.class);
              AuthProperties props = context.getBean(AuthProperties.class);
              assertThat(props.getIssuer())
                  .isEqualTo("https://issuer.example.com/realms/test");
            });
  }
}
