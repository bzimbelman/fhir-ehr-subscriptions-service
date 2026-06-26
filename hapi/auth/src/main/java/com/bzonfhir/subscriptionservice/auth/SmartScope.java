package com.bzonfhir.subscription_service.auth;

import java.util.Collections;
import java.util.EnumSet;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Locale;
import java.util.Objects;
import java.util.Set;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * Parsed representation of a single SMART on FHIR system-level scope, e.g.
 * {@code system/Subscription.crus} or {@code system/Patient.r}.
 *
 * <p>This implementation intentionally supports ONLY the {@code system/} grant level and
 * the legacy {@code .[c|r|u|d|s]} CRUDS flags used in the realm contract documented in
 * {@code docs/auth.md}. It does NOT attempt to handle the SMART v2 {@code .read/.write}
 * compact verb syntax — the realm we control issues the flag form. Tokens carrying
 * unrecognized scopes are ignored (not rejected) so the realm can evolve without breaking
 * old clients.
 */
public final class SmartScope {

  /**
   * Permission flags following the SMART convention:
   *
   * <ul>
   *   <li>{@code c} — create
   *   <li>{@code r} — read (by id)
   *   <li>{@code u} — update
   *   <li>{@code d} — delete
   *   <li>{@code s} — search (and read-history, vread)
   * </ul>
   */
  public enum Permission {
    CREATE,
    READ,
    UPDATE,
    DELETE,
    SEARCH;
  }

  /**
   * Matches {@code system/<ResourceType>.<flags>} where {@code <flags>} is a non-empty
   * sequence of the letters c/r/u/d/s in any order. Resource type starts with an uppercase
   * letter per the FHIR rule.
   */
  private static final Pattern PATTERN =
      Pattern.compile("^system/([A-Z][A-Za-z0-9]+)\\.([crudsCRUDS]+)$");

  private final String resourceType;
  private final Set<Permission> permissions;

  private SmartScope(String resourceType, Set<Permission> permissions) {
    this.resourceType = resourceType;
    this.permissions = Collections.unmodifiableSet(permissions);
  }

  public String getResourceType() {
    return resourceType;
  }

  public Set<Permission> getPermissions() {
    return permissions;
  }

  /** Convenience for callers asking "does this scope grant CREATE/READ/etc.?". */
  public boolean allows(Permission permission) {
    return permissions.contains(permission);
  }

  /**
   * Parses a single scope string. Returns {@code null} for anything we don't recognize so
   * the caller can simply drop unknown scopes rather than treat them as a 400.
   */
  public static SmartScope parse(String raw) {
    if (raw == null) {
      return null;
    }
    String trimmed = raw.trim();
    if (trimmed.isEmpty()) {
      return null;
    }
    Matcher m = PATTERN.matcher(trimmed);
    if (!m.matches()) {
      return null;
    }
    String resourceType = m.group(1);
    String flags = m.group(2).toLowerCase(Locale.ROOT);
    EnumSet<Permission> perms = EnumSet.noneOf(Permission.class);
    for (int i = 0; i < flags.length(); i++) {
      switch (flags.charAt(i)) {
        case 'c' -> perms.add(Permission.CREATE);
        case 'r' -> perms.add(Permission.READ);
        case 'u' -> perms.add(Permission.UPDATE);
        case 'd' -> perms.add(Permission.DELETE);
        case 's' -> perms.add(Permission.SEARCH);
        default -> { /* unreachable given PATTERN */ }
      }
    }
    if (perms.isEmpty()) {
      return null;
    }
    return new SmartScope(resourceType, perms);
  }

  /**
   * Parses the contents of the {@code scope} claim. Per RFC 6749 / 8693 the claim is a
   * space-delimited list of strings; we accept tabs and newlines too because some IdPs and
   * libraries normalize whitespace differently. Unknown scope strings are silently dropped.
   *
   * @param scopeClaim the raw value of the {@code scope} claim; may be {@code null}.
   * @return ordered set (preserves token order, useful for tests/logging) of recognized scopes.
   */
  public static Set<SmartScope> parseAll(String scopeClaim) {
    if (scopeClaim == null || scopeClaim.isBlank()) {
      return Collections.emptySet();
    }
    Set<SmartScope> out = new LinkedHashSet<>();
    for (String token : scopeClaim.split("\\s+")) {
      SmartScope s = parse(token);
      if (s != null) {
        out.add(s);
      }
    }
    return out;
  }

  @Override
  public boolean equals(Object o) {
    if (this == o) return true;
    if (!(o instanceof SmartScope other)) return false;
    return Objects.equals(resourceType, other.resourceType)
        && Objects.equals(permissions, other.permissions);
  }

  @Override
  public int hashCode() {
    return Objects.hash(resourceType, permissions);
  }

  @Override
  public String toString() {
    StringBuilder sb = new StringBuilder("system/").append(resourceType).append('.');
    // Render flags in CRUDS canonical order regardless of input order.
    if (permissions.contains(Permission.CREATE)) sb.append('c');
    if (permissions.contains(Permission.READ)) sb.append('r');
    if (permissions.contains(Permission.UPDATE)) sb.append('u');
    if (permissions.contains(Permission.DELETE)) sb.append('d');
    if (permissions.contains(Permission.SEARCH)) sb.append('s');
    return sb.toString();
  }

  /** Convenience for older Java callers that hand-build the list. */
  public static List<Permission> defaultReadPermissions() {
    return List.of(Permission.READ, Permission.SEARCH);
  }
}
