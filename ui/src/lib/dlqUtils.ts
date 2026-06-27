import type { MessageSummary } from "@/lib/dashboardTypes";
import type {
  DlqFilters,
  ErrorFingerprintGroup,
  TimeRange,
} from "@/lib/dlqTypes";

/**
 * Pure helpers for the DLQ viewer (Epic #398, ticket #403). No React, no
 * fetches -- trivially unit-testable. Two areas of logic live here:
 *
 *   1. Age handling: bucket rows into green / yellow / red bands so the
 *      operator can spot ancient DLQ rows at a glance.
 *   2. Error fingerprinting: collapse free-text `last_error` strings into a
 *      coarse fingerprint by lower-casing, stripping volatile tokens
 *      (URLs, UUIDs, hex blobs, numbers), and clipping to 60 chars. The
 *      goal is to surface "23 rows are failing the same way" -- not
 *      bullet-proof classification. Backend-side fingerprinting is a
 *      separate story; v1 ships with this client-side approximation.
 *   3. Filtering: collapse the user's filter selections (source system,
 *      protocol, message type, time range, error pattern) into a single
 *      predicate over `MessageSummary` rows.
 */

/** Age colour band for the list view. */
export type AgeBand = "green" | "yellow" | "red";

/**
 * Bands:
 *   - green:  < 1 hour
 *   - yellow: 1 - 24 hours
 *   - red:    > 24 hours (or unknown timestamp -- absence of evidence
 *             skews to "investigate" rather than "fresh").
 */
export function ageBand(receivedAt: string | null, now: Date): AgeBand {
  if (!receivedAt) return "red";
  const t = new Date(receivedAt).getTime();
  if (Number.isNaN(t)) return "red";
  const ageMs = now.getTime() - t;
  const ONE_HOUR = 60 * 60 * 1000;
  const ONE_DAY = 24 * ONE_HOUR;
  if (ageMs < ONE_HOUR) return "green";
  if (ageMs < ONE_DAY) return "yellow";
  return "red";
}

/**
 * Build a coarse fingerprint of a free-text error string so we can group
 * "the same error" together. Heuristic, NOT semantic — this is v1.
 *
 * Steps:
 *   1. lowercase
 *   2. strip URLs (http[s]://...)
 *   3. strip UUIDs
 *   4. strip hex blobs >=8 chars
 *   5. strip standalone numbers
 *   6. collapse repeated whitespace
 *   7. trim, take first 60 chars
 *
 * Empty / null input -> "(unknown error)" so it groups visibly.
 */
export function fingerprintError(err: string | null | undefined): string {
  if (!err) return "(unknown error)";
  let s = err.toLowerCase();
  s = s.replace(/https?:\/\/\S+/g, "<url>");
  s = s.replace(
    /\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b/g,
    "<uuid>",
  );
  s = s.replace(/\b[0-9a-f]{8,}\b/g, "<hex>");
  s = s.replace(/\b\d+\b/g, "<n>");
  s = s.replace(/\s+/g, " ").trim();
  if (s.length === 0) return "(unknown error)";
  return s.slice(0, 60);
}

/**
 * Group a page of rows by their error fingerprint and return the top N
 * groups by count (descending). Ties broken by lexical fingerprint order so
 * the output is deterministic in tests.
 */
export function groupByFingerprint(
  rows: MessageSummary[],
  topN = 5,
): ErrorFingerprintGroup[] {
  const counts = new Map<string, number>();
  for (const r of rows) {
    const fp = fingerprintError(r.last_error);
    counts.set(fp, (counts.get(fp) ?? 0) + 1);
  }
  const groups: ErrorFingerprintGroup[] = Array.from(counts.entries()).map(
    ([fingerprint, count]) => ({
      fingerprint,
      sample: fingerprint,
      count,
    }),
  );
  groups.sort((a, b) => {
    if (b.count !== a.count) return b.count - a.count;
    return a.fingerprint.localeCompare(b.fingerprint);
  });
  return groups.slice(0, topN);
}

/**
 * Predicate factory for the client-side filter pipeline. Returns true for
 * rows that should be shown given the current filter state.
 *
 * All free-text filters are case-insensitive `contains` (operators describe
 * them as "narrow until I see what I'm looking for", not regex). Time-range
 * filtering is applied client-side here -- the backend query is always
 * "DEAD_LETTER, newest first"; reducing the window further is a render-time
 * concern, not a query-tier concern.
 */
export function matchesFilters(
  row: MessageSummary,
  filters: DlqFilters,
  now: Date,
): boolean {
  if (filters.sourceSystem) {
    if (
      !row.source_system
        .toLowerCase()
        .includes(filters.sourceSystem.toLowerCase())
    ) {
      return false;
    }
  }
  if (filters.sourceProtocol !== "all") {
    if (row.source_protocol !== filters.sourceProtocol) return false;
  }
  if (filters.messageType) {
    if (
      !row.message_type
        .toLowerCase()
        .includes(filters.messageType.toLowerCase())
    ) {
      return false;
    }
  }
  if (filters.lastErrorPattern) {
    if (!errorMatchesPattern(row.last_error, filters.lastErrorPattern)) {
      return false;
    }
  }
  if (filters.timeRange !== "all") {
    if (!withinTimeRange(row.received_at, filters.timeRange, now)) return false;
  }
  return true;
}

function withinTimeRange(
  receivedAt: string | null,
  range: TimeRange,
  now: Date,
): boolean {
  if (!receivedAt) return false;
  const t = new Date(receivedAt).getTime();
  if (Number.isNaN(t)) return false;
  const ageMs = now.getTime() - t;
  switch (range) {
    case "1h":
      return ageMs < 60 * 60 * 1000;
    case "24h":
      return ageMs < 24 * 60 * 60 * 1000;
    case "7d":
      return ageMs < 7 * 24 * 60 * 60 * 1000;
    case "all":
      return true;
  }
}

/**
 * Match a row's `last_error` against the user-supplied pattern. The pattern
 * may be raw user input (free-text contains, case-insensitive) OR a
 * fingerprint string copied from the Common errors panel containing
 * placeholder tokens like `<url>`, `<uuid>`, `<hex>`, `<n>`.
 *
 * When placeholder tokens are present, we compile the pattern to a regex
 * with each placeholder mapped to a permissive class:
 *   <url>  -> https?://\S+
 *   <uuid> -> standard 8-4-4-4-12 hex
 *   <hex>  -> any hex blob of >=8 chars
 *   <n>    -> any run of digits
 * Other characters are regex-escaped. This lets a clicked fingerprint
 * round-trip and actually narrow the rendered rows.
 */
export function errorMatchesPattern(
  err: string | null | undefined,
  pattern: string,
): boolean {
  if (!pattern) return true;
  const hay = (err ?? "").toLowerCase();
  const pat = pattern.toLowerCase();
  if (!pat.includes("<")) {
    // No placeholders -- fast path: plain substring match.
    return hay.includes(pat);
  }
  // Build a regex with placeholder tokens mapped to permissive classes.
  const placeholderClasses: Record<string, string> = {
    "<url>": "https?:\\/\\/\\S+",
    "<uuid>": "[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}",
    "<hex>": "[0-9a-f]{8,}",
    "<n>": "\\d+",
  };
  // Tokenize: split on each known placeholder while preserving them.
  const tokenRegex = /(<url>|<uuid>|<hex>|<n>)/g;
  const parts = pat.split(tokenRegex);
  const compiled = parts
    .map((p) => {
      if (placeholderClasses[p]) return placeholderClasses[p];
      return p.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    })
    .join("");
  try {
    const re = new RegExp(compiled);
    return re.test(hay);
  } catch {
    // Should never happen; fall back to substring match without placeholders.
    return hay.includes(pat);
  }
}

/** Compact a long last_error into a single-line ~80-char preview. */
export function truncateError(s: string | null | undefined, max = 80): string {
  if (!s) return "";
  const oneLine = s.replace(/\s+/g, " ").trim();
  if (oneLine.length <= max) return oneLine;
  return oneLine.slice(0, max - 1) + "…";
}
