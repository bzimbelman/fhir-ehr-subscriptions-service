package com.bzonfhir.subscriptionservice.channelsecurity;

import static org.assertj.core.api.Assertions.assertThat;

import java.util.Map;

import org.junit.jupiter.api.Test;
import org.springframework.core.env.MapPropertySource;
import org.springframework.core.env.StandardEnvironment;

import com.bzonfhir.subscriptionservice.channelsecurity.ChannelSecurityProperties.ChannelSecurityMode;

/**
 * Tests for the scalar-env-var override path on {@link ChannelSecurityAutoConfiguration}.
 *
 * <p>The deploy stack sets {@code SUBSCRIPTION_SERVICE_CHANNEL_SECURITY=strict|relaxed|permissive}
 * as a single scalar; we resolve it here from the Spring {@link org.springframework.core.env.Environment}
 * because {@code @ConfigurationProperties} alone can't bind a scalar prefix into a nested field.
 *
 * <p>Ticket: #368.
 */
class ChannelSecurityAutoConfigurationTest {

  private StandardEnvironment envWith(Map<String, Object> props) {
    StandardEnvironment env = new StandardEnvironment();
    env.getPropertySources().addFirst(new MapPropertySource("test", props));
    return env;
  }

  @Test
  void scalarEnvVarStrictApplied() {
    ChannelSecurityProperties p = new ChannelSecurityProperties();
    p.setMode(ChannelSecurityMode.PERMISSIVE);
    ChannelSecurityAutoConfiguration.applyScalarOverride(
        p, envWith(Map.of("SUBSCRIPTION_SERVICE_CHANNEL_SECURITY", "strict")));
    assertThat(p.getMode()).isEqualTo(ChannelSecurityMode.STRICT);
  }

  @Test
  void scalarEnvVarRelaxedApplied() {
    ChannelSecurityProperties p = new ChannelSecurityProperties();
    ChannelSecurityAutoConfiguration.applyScalarOverride(
        p, envWith(Map.of("SUBSCRIPTION_SERVICE_CHANNEL_SECURITY", "relaxed")));
    assertThat(p.getMode()).isEqualTo(ChannelSecurityMode.RELAXED);
  }

  @Test
  void scalarEnvVarPermissiveApplied() {
    ChannelSecurityProperties p = new ChannelSecurityProperties();
    ChannelSecurityAutoConfiguration.applyScalarOverride(
        p, envWith(Map.of("SUBSCRIPTION_SERVICE_CHANNEL_SECURITY", "permissive")));
    assertThat(p.getMode()).isEqualTo(ChannelSecurityMode.PERMISSIVE);
  }

  @Test
  void scalarEnvVarLowerOrUpperCase() {
    ChannelSecurityProperties p = new ChannelSecurityProperties();
    ChannelSecurityAutoConfiguration.applyScalarOverride(
        p, envWith(Map.of("SUBSCRIPTION_SERVICE_CHANNEL_SECURITY", "PERMISSIVE")));
    assertThat(p.getMode()).isEqualTo(ChannelSecurityMode.PERMISSIVE);
  }

  @Test
  void dottedFormAlsoResolves() {
    ChannelSecurityProperties p = new ChannelSecurityProperties();
    ChannelSecurityAutoConfiguration.applyScalarOverride(
        p, envWith(Map.of("subscription-service.channel-security", "relaxed")));
    assertThat(p.getMode()).isEqualTo(ChannelSecurityMode.RELAXED);
  }

  @Test
  void invalidValueLeavesModeUnchanged() {
    ChannelSecurityProperties p = new ChannelSecurityProperties();
    p.setMode(ChannelSecurityMode.STRICT);
    ChannelSecurityAutoConfiguration.applyScalarOverride(
        p, envWith(Map.of("SUBSCRIPTION_SERVICE_CHANNEL_SECURITY", "nope")));
    assertThat(p.getMode()).isEqualTo(ChannelSecurityMode.STRICT);
  }

  @Test
  void missingEnvVarLeavesModeUnchanged() {
    ChannelSecurityProperties p = new ChannelSecurityProperties();
    p.setMode(ChannelSecurityMode.RELAXED);
    ChannelSecurityAutoConfiguration.applyScalarOverride(p, envWith(Map.of()));
    assertThat(p.getMode()).isEqualTo(ChannelSecurityMode.RELAXED);
  }
}
