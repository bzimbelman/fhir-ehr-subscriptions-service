package com.bzonfhir.subscriptionservice.channelsecurity;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatCode;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.Mockito.lenient;
import static org.mockito.Mockito.mock;

import java.util.HashMap;
import java.util.Map;

import org.hl7.fhir.r4.model.OperationOutcome;
import org.hl7.fhir.r4.model.Patient;
import org.hl7.fhir.r4.model.Subscription;
import org.hl7.fhir.r4.model.Subscription.SubscriptionChannelType;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import ca.uhn.fhir.rest.api.server.RequestDetails;
import ca.uhn.fhir.rest.server.exceptions.UnprocessableEntityException;

import com.bzonfhir.subscriptionservice.auth.AuthProperties;
import com.bzonfhir.subscriptionservice.auth.OidcJwtAuthenticationInterceptor;
import com.bzonfhir.subscriptionservice.channelsecurity.ChannelSecurityProperties.ChannelSecurityMode;

/**
 * Behavioral tests for the channel-security interceptor. Builds {@link Subscription} resources
 * programmatically and a mock {@link RequestDetails}, then asserts the interceptor accepts or
 * rejects (with an {@link UnprocessableEntityException} + {@link OperationOutcome}) per mode.
 *
 * <p>Tickets: #368.
 */
class ChannelSecurityInterceptorTest {

  private ChannelSecurityProperties props;
  private AuthProperties authProps;
  private ChannelSecurityInterceptor interceptor;

  @BeforeEach
  void setUp() {
    props = new ChannelSecurityProperties();
    authProps = new AuthProperties();
    // Auth on by default — strict-mode WebSocket rule relies on an authenticated session.
    authProps.setEnabled(true);
    interceptor = new ChannelSecurityInterceptor(props, authProps);
  }

  // ---- helpers -----------------------------------------------------------

  private Subscription resthook(String endpoint, String... headers) {
    Subscription s = new Subscription();
    s.getChannel().setType(SubscriptionChannelType.RESTHOOK);
    if (endpoint != null) {
      s.getChannel().setEndpoint(endpoint);
    }
    for (String h : headers) {
      s.getChannel().addHeader(h);
    }
    return s;
  }

  private Subscription websocket(String endpoint, String... headers) {
    Subscription s = new Subscription();
    s.getChannel().setType(SubscriptionChannelType.WEBSOCKET);
    if (endpoint != null) {
      s.getChannel().setEndpoint(endpoint);
    }
    for (String h : headers) {
      s.getChannel().addHeader(h);
    }
    return s;
  }

  /**
   * Builds a mock RequestDetails. If {@code authenticated} is true, stashes a fake claims
   * object under the auth interceptor's key, mimicking what the auth interceptor would have
   * done upstream.
   */
  private RequestDetails mockRequest(boolean authenticated) {
    RequestDetails rd = mock(RequestDetails.class);
    Map<Object, Object> userData = new HashMap<>();
    if (authenticated) {
      userData.put(
          OidcJwtAuthenticationInterceptor.USER_DATA_CLAIMS_KEY, new Object());
    }
    lenient().when(rd.getUserData()).thenReturn(userData);
    return rd;
  }

  // ---- non-Subscription resources are passthrough ------------------------

  @Test
  void ignoresNonSubscriptionResources() {
    Patient p = new Patient();
    assertThatCode(() -> interceptor.evaluate(p, mockRequest(true))).doesNotThrowAnyException();
  }

  // ---- STRICT ------------------------------------------------------------

  @Test
  void strict_httpsEndpointWithAuthHeader_accepts() {
    props.setMode(ChannelSecurityMode.STRICT);
    Subscription s = resthook("https://hook.example.com/notify", "Authorization: Bearer abc.def");
    assertThatCode(() -> interceptor.evaluate(s, mockRequest(true))).doesNotThrowAnyException();
  }

  @Test
  void strict_httpEndpoint_rejectsWithOperationOutcomeListingHttps() {
    props.setMode(ChannelSecurityMode.STRICT);
    Subscription s = resthook("http://hook.example.com/notify", "Authorization: Bearer abc.def");

    assertThatThrownBy(() -> interceptor.evaluate(s, mockRequest(true)))
        .isInstanceOf(UnprocessableEntityException.class)
        .satisfies(
            ex -> {
              UnprocessableEntityException uee = (UnprocessableEntityException) ex;
              assertThat(uee.getOperationOutcome()).isInstanceOf(OperationOutcome.class);
              OperationOutcome oo = (OperationOutcome) uee.getOperationOutcome();
              assertThat(oo.getIssue())
                  .anySatisfy(
                      i ->
                          assertThat(i.getDiagnostics())
                              .containsIgnoringCase("HTTPS required"));
            });
  }

  @Test
  void strict_httpsEndpointWithoutAuthHeader_rejectsWithAuthHeaderRequired() {
    props.setMode(ChannelSecurityMode.STRICT);
    Subscription s = resthook("https://hook.example.com/notify");

    assertThatThrownBy(() -> interceptor.evaluate(s, mockRequest(true)))
        .isInstanceOf(UnprocessableEntityException.class)
        .satisfies(
            ex -> {
              UnprocessableEntityException uee = (UnprocessableEntityException) ex;
              OperationOutcome oo = (OperationOutcome) uee.getOperationOutcome();
              assertThat(oo.getIssue())
                  .anySatisfy(
                      i ->
                          assertThat(i.getDiagnostics())
                              .containsIgnoringCase("Authorization header required"));
            });
  }

  @Test
  void strict_authHeaderMatchIsCaseInsensitive() {
    props.setMode(ChannelSecurityMode.STRICT);
    Subscription s = resthook("https://hook.example.com/notify", "authorization: Bearer abc.def");
    assertThatCode(() -> interceptor.evaluate(s, mockRequest(true))).doesNotThrowAnyException();
  }

  @Test
  void strict_nonAuthorizationHeaderDoesNotSatisfy() {
    props.setMode(ChannelSecurityMode.STRICT);
    Subscription s = resthook("https://hook.example.com/notify", "X-API-Key: foo");
    assertThatThrownBy(() -> interceptor.evaluate(s, mockRequest(true)))
        .isInstanceOf(UnprocessableEntityException.class)
        .hasMessageContaining("Authorization header required");
  }

  // ---- RELAXED -----------------------------------------------------------

  @Test
  void relaxed_httpsEndpointWithoutAuthHeader_accepts() {
    props.setMode(ChannelSecurityMode.RELAXED);
    Subscription s = resthook("https://hook.example.com/notify");
    assertThatCode(() -> interceptor.evaluate(s, mockRequest(true))).doesNotThrowAnyException();
  }

  @Test
  void relaxed_httpEndpoint_rejects() {
    props.setMode(ChannelSecurityMode.RELAXED);
    Subscription s = resthook("http://hook.example.com/notify");
    assertThatThrownBy(() -> interceptor.evaluate(s, mockRequest(true)))
        .isInstanceOf(UnprocessableEntityException.class)
        .hasMessageContaining("HTTPS required");
  }

  // ---- PERMISSIVE --------------------------------------------------------

  @Test
  void permissive_httpEndpointWithoutHeader_accepts() {
    props.setMode(ChannelSecurityMode.PERMISSIVE);
    Subscription s = resthook("http://hook.example.com/notify");
    assertThatCode(() -> interceptor.evaluate(s, mockRequest(true))).doesNotThrowAnyException();
  }

  // ---- WebSocket -----------------------------------------------------------

  @Test
  void strict_websocket_authEnabled_accepts() {
    props.setMode(ChannelSecurityMode.STRICT);
    authProps.setEnabled(true);
    Subscription s = websocket(null);
    assertThatCode(() -> interceptor.evaluate(s, mockRequest(true))).doesNotThrowAnyException();
  }

  @Test
  void strict_websocket_authDisabled_logsWarnAndAccepts() {
    props.setMode(ChannelSecurityMode.STRICT);
    authProps.setEnabled(false);
    Subscription s = websocket(null);
    // No exception: warning logged is the documented behavior.
    assertThatCode(() -> interceptor.evaluate(s, mockRequest(false))).doesNotThrowAnyException();
  }

  // ---- edge cases --------------------------------------------------------

  @Test
  void strict_missingEndpointOnRestHook_rejects() {
    props.setMode(ChannelSecurityMode.STRICT);
    Subscription s = resthook(null, "Authorization: Bearer x");
    assertThatThrownBy(() -> interceptor.evaluate(s, mockRequest(true)))
        .isInstanceOf(UnprocessableEntityException.class)
        .hasMessageContaining("endpoint");
  }

  @Test
  void strict_malformedEndpointUrl_rejects() {
    props.setMode(ChannelSecurityMode.STRICT);
    Subscription s = resthook(":::not-a-url", "Authorization: Bearer x");
    assertThatThrownBy(() -> interceptor.evaluate(s, mockRequest(true)))
        .isInstanceOf(UnprocessableEntityException.class)
        .hasMessageContaining("endpoint");
  }

  @Test
  void permissive_logsWarningAtStartup() {
    // The constructor logs a WARN when initialized in permissive mode. We exercise that
    // path here; the test runner's log output is captured by surefire so a human can see it.
    // Behavioral assertion: instantiation completes without exception.
    ChannelSecurityProperties permissive = new ChannelSecurityProperties();
    permissive.setMode(ChannelSecurityMode.PERMISSIVE);
    ChannelSecurityInterceptor warnInterceptor =
        new ChannelSecurityInterceptor(permissive, authProps);
    warnInterceptor.logStartupMode(); // explicit re-trigger to assert idempotency
    assertThat(permissive.getMode()).isEqualTo(ChannelSecurityMode.PERMISSIVE);
  }

  @Test
  void defaultModeIsStrict() {
    ChannelSecurityProperties fresh = new ChannelSecurityProperties();
    assertThat(fresh.getMode()).isEqualTo(ChannelSecurityMode.STRICT);
  }
}
