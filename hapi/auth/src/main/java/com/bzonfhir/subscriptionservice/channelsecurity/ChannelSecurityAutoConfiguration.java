package com.bzonfhir.subscriptionservice.channelsecurity;

import java.util.Locale;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.SmartInitializingSingleton;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.autoconfigure.AutoConfiguration;
import org.springframework.boot.context.properties.EnableConfigurationProperties;
import org.springframework.context.annotation.Bean;
import org.springframework.core.env.Environment;

import ca.uhn.fhir.rest.server.RestfulServer;

import com.bzonfhir.subscriptionservice.auth.AuthProperties;
import com.bzonfhir.subscriptionservice.channelsecurity.ChannelSecurityProperties.ChannelSecurityMode;

/**
 * Spring Boot auto-configuration for the subscription-service channel-security layer.
 *
 * <p>Always loaded (no {@code @ConditionalOnProperty}) — the interceptor wires regardless of
 * the configured mode. The mode itself, including the new {@code permissive} relaxation, is
 * what determines runtime behavior. This keeps the failure mode obvious: a misconfigured
 * {@code SUBSCRIPTION_SERVICE_CHANNEL_SECURITY} can never silently disable the interceptor.
 *
 * <p>Registered via {@code META-INF/spring/org.springframework.boot.autoconfigure.AutoConfiguration.imports}.
 *
 * <p>Two operator-facing knobs are supported:
 *
 * <ul>
 *   <li>{@code SUBSCRIPTION_SERVICE_CHANNEL_SECURITY=strict|relaxed|permissive} — scalar env
 *       var, as documented in {@code docs/architecture.md}. Resolved by reading the Spring
 *       {@link Environment} directly inside {@link #channelSecurityInterceptor} because
 *       {@code @ConfigurationProperties} doesn't bind a scalar prefix to a nested field.
 *   <li>{@code subscription-service.channel-security.mode: strict|relaxed|permissive} — the
 *       nested-YAML form picked up by {@link ChannelSecurityProperties}.
 * </ul>
 *
 * <p>The scalar env var wins when both are set so operators can override at deploy time.
 *
 * <p>Ticket: #368.
 */
@AutoConfiguration
@EnableConfigurationProperties({ChannelSecurityProperties.class, AuthProperties.class})
public class ChannelSecurityAutoConfiguration {

  private static final Logger log =
      LoggerFactory.getLogger(ChannelSecurityAutoConfiguration.class);

  /**
   * Property keys checked, in order, to resolve the scalar form of the env var. Spring's
   * {@code SystemEnvironmentPropertySource} normalizes {@code SUBSCRIPTION_SERVICE_CHANNEL_SECURITY}
   * to all of these candidates; we check them explicitly so the resolution is deterministic
   * and testable.
   */
  static final String[] SCALAR_PROPERTY_KEYS = {
    "SUBSCRIPTION_SERVICE_CHANNEL_SECURITY",
    "subscription.service.channel.security",
    "subscription-service.channel-security",
  };

  @Bean
  public ChannelSecurityInterceptor channelSecurityInterceptor(
      ChannelSecurityProperties props, AuthProperties authProps, Environment env) {
    applyScalarOverride(props, env);
    return new ChannelSecurityInterceptor(props, authProps);
  }

  /**
   * If the operator supplied the documented scalar env var {@code SUBSCRIPTION_SERVICE_CHANNEL_SECURITY},
   * apply it. Visible for tests.
   */
  static void applyScalarOverride(ChannelSecurityProperties props, Environment env) {
    for (String key : SCALAR_PROPERTY_KEYS) {
      String raw = env.getProperty(key);
      if (raw == null || raw.isBlank()) {
        continue;
      }
      try {
        ChannelSecurityMode parsed =
            ChannelSecurityMode.valueOf(raw.trim().toUpperCase(Locale.ROOT));
        props.setMode(parsed);
        return;
      } catch (IllegalArgumentException e) {
        log.warn(
            "Invalid value '{}' for {}; expected one of strict|relaxed|permissive. "
                + "Falling back to {}.",
            raw,
            key,
            props.getMode());
        return;
      }
    }
  }

  /**
   * Mirrors the registrar pattern used by the auth module: arbitrary {@code IServerInterceptor}
   * beans aren't auto-registered on HAPI's {@link RestfulServer} unless they're listed under
   * {@code hapi.fhir.custom-interceptor-classes}. Using a {@link SmartInitializingSingleton}
   * sidesteps that requirement.
   */
  @Bean
  public SmartInitializingSingleton channelSecurityInterceptorRegistrar(
      @Autowired(required = false) RestfulServer restfulServer,
      ChannelSecurityInterceptor interceptor) {
    return () -> {
      if (restfulServer == null) {
        log.warn(
            "No RestfulServer bean found — channel-security interceptor NOT registered. "
                + "Fine for unit tests; indicates a packaging problem inside the HAPI image.");
        return;
      }
      restfulServer.registerInterceptor(interceptor);
      log.info(
          "Registered subscription-service channel-security interceptor on HAPI RestfulServer.");
    };
  }
}
