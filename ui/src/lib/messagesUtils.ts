import type { MessageSummary } from "@/lib/dashboardTypes";
import type { MessagesListFilters, TimeRange } from "@/lib/messagesTypes";

/**
 * Pure helpers for the message viewer (Epic #398, ticket #402).
 *
 * No React imports — tested in isolation. The filtering rules:
 *
 *   - status: applied SERVER-SIDE via the `status` query param; rows in the
 *     fetched page already match the filter, so this helper ignores it.
 *     The view re-fetches when the dropdown changes.
 *   - sourceSystem / messageType: client-side "contains" (case-insensitive).
 *     A free-text filter that narrows the visible page.
 *   - timeRange: client-side cutoff against `received_at`. The backend
 *     doesn't yet expose a `received_after` param; documented limitation.
 */

/** True when `received_at` falls inside the selected window relative to `now`. */
export function withinTimeRange(
  receivedAt: string | null | undefined,
  range: TimeRange,
  now: Date,
): boolean {
  if (range === "all") return true;
  if (!receivedAt) return false;
  const ts = new Date(receivedAt).getTime();
  if (Number.isNaN(ts)) return false;

  let cutoffMs: number;
  switch (range) {
    case "today": {
      // Local-day window: anything received on or after midnight local.
      const start = new Date(now);
      start.setHours(0, 0, 0, 0);
      cutoffMs = start.getTime();
      break;
    }
    case "24h":
      cutoffMs = now.getTime() - 24 * 60 * 60 * 1000;
      break;
    case "7d":
      cutoffMs = now.getTime() - 7 * 24 * 60 * 60 * 1000;
      break;
    default:
      cutoffMs = -Infinity;
  }

  return ts >= cutoffMs;
}

/**
 * Apply client-side filters (sourceSystem contains, messageType contains,
 * timeRange) to a list of rows. The `status` filter is intentionally
 * server-side (see module doc); we don't double-apply it here, which would
 * silently mask a backend bug.
 */
export function applyClientFilters(
  rows: readonly MessageSummary[],
  filters: MessagesListFilters,
  now: Date,
): MessageSummary[] {
  const ss = filters.sourceSystem.trim().toLowerCase();
  const mt = filters.messageType.trim().toLowerCase();
  return rows.filter((r) => {
    if (ss && !r.source_system.toLowerCase().includes(ss)) return false;
    if (mt && !r.message_type.toLowerCase().includes(mt)) return false;
    if (!withinTimeRange(r.received_at, filters.timeRange, now)) return false;
    return true;
  });
}

/**
 * Truncate `last_error` for the table cell. The full text is rendered in
 * the row's `title` attribute (browser tooltip) and on the detail page.
 */
export function truncateError(s: string | null | undefined, max = 60): string {
  if (!s) return "";
  if (s.length <= max) return s;
  return s.slice(0, max - 1) + "…";
}

/**
 * Compute duration_ms between received_at and delivered_at, where both are
 * present and parseable. Returns null otherwise so the UI can render "—".
 */
export function durationMs(
  receivedAt: string | null | undefined,
  deliveredAt: string | null | undefined,
): number | null {
  if (!receivedAt || !deliveredAt) return null;
  const r = new Date(receivedAt).getTime();
  const d = new Date(deliveredAt).getTime();
  if (Number.isNaN(r) || Number.isNaN(d)) return null;
  const out = d - r;
  if (out < 0) return null;
  return out;
}
