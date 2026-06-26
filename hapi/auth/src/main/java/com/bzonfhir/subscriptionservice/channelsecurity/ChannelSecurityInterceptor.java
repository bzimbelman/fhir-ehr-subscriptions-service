package com.bzonfhir.subscriptionservice.channelsecurity;

import java.net.URI;
import java.net.URISyntaxException;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;

import org.hl7.fhir.instance.model.api.IBaseResource;
import org.hl7.fhir.r4.model.OperationOutcome;
import org.hl7.fhir.r4.model.OperationOutcome.IssueSeverity;
import org.hl7.fhir.r4.model.OperationOutcome.IssueType;
import org.hl7.fhir.r4.model.OperationOutcome.OperationOutcomeIssueComponent;
import org.hl7.fhir.r4.model.StringType;
import org.hl7.fhir.r4.model.Subscription;
import org.hl7.fhir.r4.model.Subscription.SubscriptionChannelComponent;
import org.hl7.fhir.r4.model.Subscription.SubscriptionChannelType;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import ca.uhn.fhir.interceptor.api.Hook;
import ca.uhn.fhir.interceptor.api.Interceptor;
import ca.uhn.fhir.interceptor.api.Pointcut;
import ca.uhn.fhir.rest.api.server.RequestDetails;
import ca.uhn.fhir.rest.server.exceptions.UnprocessableEntityException;

import com.bzonfhir.subscriptionservice.auth.AuthProperties;
import com.bzonfhir.subscriptionservice.auth.KeycloakJwtAuthenticationInterceptor;
import com.bzonfhir.subscriptionservice.channelsecurity.ChannelSecurityProperties.ChannelSecurityMode;

/**
 * HAPI interceptor that validates the {@code channel} block of incoming {@link Subscription}
 * resources against the configured {@link ChannelSecurityMode}.
 *
 * <p>Wired on the JPA pre-storage create/update pointcuts so the request never reaches the
 * subscription matcher / persistence layer when the policy is violated. Rejection produces
 * an HTTP 422 (Unprocessable Entity) plus an {@link OperationOutcome} that lists each
 * specific violation as a separate issue — clients get an actionable error, not a generic
 * "invalid subscription".
 *
 * <p>Modes are described on {@link ChannelSecurityProperties}; the architecture decision
 * record is {@code docs/architecture.md} "Subscription channel security".
 *
 * <p>Ticket: #368.
 */
@Interceptor
public class ChannelSecurityInterceptor {

  private static final Logger log = LoggerFactory.getLogger(ChannelSecurityInterceptor.class);
  private static final String AUTH_HEADER_PREFIX = "authorization:";

  private final ChannelSecurityProperties props;
  private final AuthProperties authProps;

  public ChannelSecurityInterceptor(ChannelSecurityProperties props, AuthProperties authProps) {
    this.props = props;
    this.authProps = authProps;
    logStartupMode();
  }

  /**
   * Emits a startup INFO/WARN documenting the active mode. Called from the constructor and
   * exposed for tests. Idempotent — safe to call repeatedly.
   */
  public void logStartupMode() {
    ChannelSecurityMode mode = props.getMode();
    if (mode == ChannelSecurityMode.PERMISSIVE) {
      log.warn(
          "Subscription channel-security mode is PERMISSIVE: HTTP endpoints and "
              + "header-less Subscriptions are accepted. This is intended for sandbox / "
              + "local-dev deployments only — do NOT use in production.");
    } else {
      log.info("Subscription channel-security mode is {}.", mode);
    }
  }

  // -- Pointcuts: HAPI invokes these on every JPA create/update --------------

  @Hook(Pointcut.STORAGE_PRESTORAGE_RESOURCE_CREATED)
  public void preCreate(IBaseResource theResource, RequestDetails theRequest) {
    evaluate(theResource, theRequest);
  }

  @Hook(Pointcut.STORAGE_PRESTORAGE_RESOURCE_UPDATED)
  public void preUpdate(
      IBaseResource theOldResource, IBaseResource theNewResource, RequestDetails theRequest) {
    // For updates HAPI delivers both old and new; we only police the new state.
    evaluate(theNewResource, theRequest);
  }

  // -- Core policy ----------------------------------------------------------

  /**
   * Inspects {@code resource}; if it's a {@link Subscription}, runs the policy checks for the
   * active mode and throws {@link UnprocessableEntityException} on violation. Non-Subscription
   * resources are a no-op. Package-private + non-final so tests can drive it directly without
   * a real HAPI dispatch.
   */
  void evaluate(IBaseResource resource, RequestDetails request) {
    if (!(resource instanceof Subscription subscription)) {
      return;
    }
    SubscriptionChannelComponent channel = subscription.getChannel();
    SubscriptionChannelType type = channel != null ? channel.getType() : null;
    ChannelSecurityMode mode = props.getMode();
    List<String> violations = new ArrayList<>();

    if (type == SubscriptionChannelType.WEBSOCKET) {
      evaluateWebSocket(request, mode, violations);
    } else {
      // REST-hook is the default and the only other mode we've productized today; other
      // channel types (email, sms, message) fall through the same URL/header logic — they
      // all carry an endpoint URL and may carry headers.
      evaluateRestLikeChannel(channel, mode, violations);
    }

    if (!violations.isEmpty()) {
      throw new UnprocessableEntityException(
          summarize(violations), buildOperationOutcome(violations));
    }
  }

  private void evaluateRestLikeChannel(
      SubscriptionChannelComponent channel, ChannelSecurityMode mode, List<String> violations) {
    String endpoint = channel == null ? null : channel.getEndpoint();
    if (endpoint == null || endpoint.isBlank()) {
      violations.add("Subscription.channel.endpoint is required");
      return;
    }

    URI uri;
    try {
      uri = new URI(endpoint);
    } catch (URISyntaxException e) {
      violations.add(
          "Subscription.channel.endpoint is not a valid URL: " + e.getMessage());
      return;
    }
    String scheme = uri.getScheme();
    if (scheme == null || scheme.isBlank()) {
      violations.add(
          "Subscription.channel.endpoint must be an absolute URL with a scheme");
      return;
    }
    scheme = scheme.toLowerCase(Locale.ROOT);

    boolean httpsRequired =
        mode == ChannelSecurityMode.STRICT || mode == ChannelSecurityMode.RELAXED;
    if (httpsRequired && !"https".equals(scheme)) {
      violations.add(
          "Subscription.channel.endpoint scheme '"
              + scheme
              + "' is not allowed: HTTPS required by channel-security mode "
              + mode.name().toLowerCase(Locale.ROOT));
    }

    boolean headerRequired = mode == ChannelSecurityMode.STRICT;
    if (headerRequired && !hasAuthorizationHeader(channel)) {
      violations.add(
          "Authorization header required: Subscription.channel.header must "
              + "contain an Authorization entry in channel-security mode strict");
    }
  }

  private void evaluateWebSocket(
      RequestDetails request, ChannelSecurityMode mode, List<String> violations) {
    if (mode != ChannelSecurityMode.STRICT) {
      return;
    }
    // For WebSocket the URL-based check doesn't apply. Instead require that the request
    // creating the Subscription was itself authenticated — re-use the auth interceptor's
    // contract: it stashes claims under USER_DATA_CLAIMS_KEY on every authenticated request.
    if (!authProps.isEnabled()) {
      log.warn(
          "Channel-security mode is STRICT but subscription-service.auth.enabled=false; "
              + "WebSocket Subscription accepted without an authenticated session. This is "
              + "a misconfiguration in production.");
      return;
    }
    Object claims = request == null ? null : request.getUserData().get(
        KeycloakJwtAuthenticationInterceptor.USER_DATA_CLAIMS_KEY);
    if (claims == null) {
      violations.add(
          "WebSocket Subscriptions require an authenticated session in "
              + "channel-security mode strict");
    }
  }

  private static boolean hasAuthorizationHeader(SubscriptionChannelComponent channel) {
    if (channel == null || !channel.hasHeader()) {
      return false;
    }
    for (StringType h : channel.getHeader()) {
      if (h == null || h.getValue() == null) continue;
      String value = h.getValue().trim();
      if (value.toLowerCase(Locale.ROOT).startsWith(AUTH_HEADER_PREFIX)) {
        return true;
      }
    }
    return false;
  }

  private static String summarize(List<String> violations) {
    if (violations.size() == 1) {
      return violations.get(0);
    }
    return "Subscription rejected by channel-security policy: "
        + String.join("; ", violations);
  }

  private static OperationOutcome buildOperationOutcome(List<String> violations) {
    OperationOutcome oo = new OperationOutcome();
    for (String v : violations) {
      OperationOutcomeIssueComponent issue = oo.addIssue();
      issue.setSeverity(IssueSeverity.ERROR);
      issue.setCode(IssueType.BUSINESSRULE);
      issue.setDiagnostics(v);
    }
    return oo;
  }
}
