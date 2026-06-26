package com.bzonfhir.subscriptionservice.validation;

import static org.assertj.core.api.Assertions.assertThat;

import org.junit.jupiter.api.Test;

/**
 * Behavioral tests for the {@link ValidationProperties} POJO. We only test the parts that
 * matter for the auto-configuration contract: the default mode and the enum's accepted values.
 *
 * <p>The Spring property-binding glue itself (env var → setter) is exercised by the
 * auto-configuration tests via {@code ApplicationContextRunner}; duplicating that here adds no
 * coverage.
 */
class ValidationPropertiesTest {

  @Test
  void defaultsToOff() {
    ValidationProperties props = new ValidationProperties();
    assertThat(props.getMode()).isEqualTo(ValidationProperties.ValidationMode.OFF);
  }

  @Test
  void modeIsSettable() {
    ValidationProperties props = new ValidationProperties();
    props.setMode(ValidationProperties.ValidationMode.WARN);
    assertThat(props.getMode()).isEqualTo(ValidationProperties.ValidationMode.WARN);
    props.setMode(ValidationProperties.ValidationMode.ENFORCE);
    assertThat(props.getMode()).isEqualTo(ValidationProperties.ValidationMode.ENFORCE);
  }

  @Test
  void enumExposesThreeValues() {
    assertThat(ValidationProperties.ValidationMode.values())
        .containsExactlyInAnyOrder(
            ValidationProperties.ValidationMode.OFF,
            ValidationProperties.ValidationMode.WARN,
            ValidationProperties.ValidationMode.ENFORCE);
  }
}
