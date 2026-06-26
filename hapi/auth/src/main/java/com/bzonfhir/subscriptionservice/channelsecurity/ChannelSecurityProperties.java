package com.bzonfhir.subscriptionservice.channelsecurity;

import org.springframework.boot.context.properties.ConfigurationProperties;

/**
 * Configuration for the subscription-service channel-security policy.
 *
 * <p>Two binding paths are supported so operators can configure the policy by setting a single
 * scalar env var or by writing nested YAML:
 *
 * <ul>
 *   <li>{@code SUBSCRIPTION_SERVICE_CHANNEL_SECURITY=strict} — Spring's relaxed binding maps
 *       this to {@code subscription-service.channel-security} (treated here as the
 *       {@link #mode} value via the dedicated setter on
 *       {@code SubscriptionServiceProperties}).
 *   <li>{@code subscription-service.channel-security.mode: strict} in {@code application.yaml}
 *       — the nested form. The {@link ConfigurationProperties} prefix below picks it up.
 * </ul>
 *
 * <p>Wired through the deploy stack at {@code deploy/docker/docker-compose.yml} and the Helm
 * chart. Modes (see {@code docs/architecture.md} "Subscription channel security"):
 *
 * <ul>
 *   <li>{@link ChannelSecurityMode#STRICT} (default) — REST-hook endpoint MUST be HTTPS; an
 *       {@code Authorization} header on the Subscription is required. WebSocket subscriptions
 *       require an authenticated session.
 *   <li>{@link ChannelSecurityMode#RELAXED} — HTTPS still required; no header mandate.
 *   <li>{@link ChannelSecurityMode#PERMISSIVE} — HTTP allowed; no header mandate. Sandbox /
 *       local-dev only; the interceptor logs a startup WARN to make this visible.
 * </ul>
 *
 * <p>Ticket: #368.
 */
@ConfigurationProperties(prefix = "subscription-service.channel-security")
public class ChannelSecurityProperties {

  /** Policy tiers in increasing-permissiveness order. */
  public enum ChannelSecurityMode {
    STRICT,
    RELAXED,
    PERMISSIVE
  }

  /** Default: secure-by-default per {@code docs/architecture.md}. */
  private ChannelSecurityMode mode = ChannelSecurityMode.STRICT;

  public ChannelSecurityMode getMode() {
    return mode;
  }

  public void setMode(ChannelSecurityMode mode) {
    this.mode = (mode == null ? ChannelSecurityMode.STRICT : mode);
  }
}
