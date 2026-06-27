import type { SubscriptionHealthRow } from "@/lib/subscriptionTypes";

/**
 * Pure formatting helpers for the subscription views. Pulled into a
 * no-React module so they're trivially unit-testable.
 *
 * The "delivery success rate" rendering carries the HAPI 7.6 caveat
 * documented in `docs/admin-api.md`: the `$status` operation isn't
 * wired, so both counters always come back as 0/0 today. The product
 * decision (ticket #404 spec): when total == 0, render an em-dash
 * "—" rather than the literal "0%". That way operators don't
 * misread "0%" as "everything is failing".
 */

const EM_DASH = "—";

export function formatDeliverySuccessRate(row: SubscriptionHealthRow): string {
  const total = row.delivery_success_count + row.delivery_failure_count;
  if (total === 0) return EM_DASH;
  const rate = row.delivery_success_count / total;
  return `${(rate * 100).toFixed(1)}%`;
}

/**
 * Truncate a string for column display, attaching the full value on
 * the `title` attribute (set at the call site). Mirrors what the
 * dashboard does for `source_id`.
 */
export function truncate(value: string | null | undefined, max = 50): string {
  if (!value) return EM_DASH;
  if (value.length <= max) return value;
  return value.slice(0, max - 1) + "…";
}

/**
 * Relative-time formatter — same shape as the dashboard's helper but
 * reproduced here to avoid a layering dependency on the dashboard
 * module (subscriptions and dashboard are sibling concerns).
 */
export function relativeTime(iso: string | null, now: Date = new Date()): string {
  if (!iso) return EM_DASH;
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return EM_DASH;
  const diffSec = Math.max(0, Math.round((now.getTime() - t) / 1000));
  if (diffSec < 5) return "just now";
  if (diffSec < 60) return `${diffSec}s ago`;
  if (diffSec < 3600) return `${Math.round(diffSec / 60)} min ago`;
  if (diffSec < 86400) return `${Math.round(diffSec / 3600)} hr ago`;
  return `${Math.round(diffSec / 86400)} day ago`;
}

/**
 * Pull the bare HAPI id out of a `Subscription/123` reference, used
 * for routing to /subscriptions/[id]. The route segment uses the bare
 * id so the URL doesn't carry a slash.
 */
export function routeIdFor(reference: string): string {
  const idx = reference.indexOf("/");
  if (idx === -1) return reference;
  return reference.slice(idx + 1);
}
