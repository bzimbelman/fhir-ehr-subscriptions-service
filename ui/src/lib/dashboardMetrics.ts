import type {
  ActuatorHealthResponse,
  ComponentHealthRow,
  ObserveSystem,
  ObserveThroughput,
  SubscriptionsHealthResponse,
  SystemStatus,
  ThroughputBucket,
} from "@/lib/dashboardTypes";

/**
 * Pure functions that derive dashboard display values from raw admin-API
 * responses. Kept in a no-React module so they're trivially unit-testable
 * without setting up jsdom + RTL.
 *
 * These functions intentionally return safe defaults (zeros, "UNKNOWN")
 * when input is missing -- the dashboard renders even when one of its
 * three upstream calls failed, so an error in `/admin/observe/throughput`
 * doesn't blank out the component-health list.
 */

const DELIVERED_STATUSES = new Set(["DELIVERED"]);
const RECEIVED_STATUSES = new Set([
  "RECEIVED",
  "TRANSFORMING",
  "DELIVERED",
  "FAILED",
  "DEAD_LETTER",
]);

/**
 * Total messages flowed through the engine in a sliding window. The throughput
 * endpoint returns buckets of {status -> count}; we sum every count across
 * every bucket.
 */
export function totalMessagesIn(throughput: ObserveThroughput | null): number {
  if (!throughput) return 0;
  let total = 0;
  for (const bucket of throughput.buckets) {
    for (const n of Object.values(bucket.counts)) {
      if (typeof n === "number") total += n;
    }
  }
  return total;
}

/**
 * Success rate over a window: delivered / (received + delivered + failed + ...)
 * Returns a fraction in [0, 1], or null when there's no data to divide by
 * (we render "--" in that case).
 */
export function successRate(
  throughput: ObserveThroughput | null,
): number | null {
  if (!throughput) return null;
  let delivered = 0;
  let total = 0;
  for (const bucket of throughput.buckets) {
    for (const [status, n] of Object.entries(bucket.counts)) {
      if (typeof n !== "number") continue;
      if (RECEIVED_STATUSES.has(status)) {
        total += n;
        if (DELIVERED_STATUSES.has(status)) delivered += n;
      }
    }
  }
  if (total === 0) return null;
  return delivered / total;
}

/** Format a success rate fraction (0..1) as a "99.2%" string, or "--" if null. */
export function formatRate(rate: number | null): string {
  if (rate === null) return "--";
  return `${(rate * 100).toFixed(1)}%`;
}

/** DLQ size from the observe/system response. Zero when missing. */
export function dlqSize(system: ObserveSystem | null): number {
  return system?.queue.dead_letter ?? 0;
}

/**
 * Active subscriptions: count of items where active=true. We surface this
 * (rather than just total) because an inactive subscription is operationally
 * silent and not "the system is doing work" data.
 */
export function activeSubscriptionsCount(
  subs: SubscriptionsHealthResponse | null,
): number {
  if (!subs) return 0;
  return subs.items.filter((s) => s.active).length;
}

/**
 * Compute the rolled-up system status pill colour from the available
 * signals. The rule (least-favourable wins):
 *   - any component DOWN  -> RED
 *   - any component DEGRADED -> YELLOW
 *   - all components UP -> GREEN
 *   - nothing known  -> UNKNOWN (rendered grey)
 *
 * Input prefers the observe/system `components` array (richer, with
 * timestamps); falls back to the actuator response when components is
 * absent or empty.
 */
export function rollupSystemStatus(
  system: ObserveSystem | null,
  actuator: ActuatorHealthResponse | null,
): SystemStatus {
  const components = componentHealthRows(system, actuator);
  if (components.length === 0) return "UNKNOWN";
  let worst: "UP" | "DEGRADED" = "UP";
  for (const c of components) {
    if (c.status === "DOWN") return "DOWN";
    if (c.status === "DEGRADED") worst = "DEGRADED";
  }
  return worst;
}

/**
 * Component-health rows for the two-column section. Prefers the richer
 * shape on observe/system; otherwise translates the actuator/health output
 * (which maps strings like "UP" / "DOWN" / "OUT_OF_SERVICE").
 */
export function componentHealthRows(
  system: ObserveSystem | null,
  actuator: ActuatorHealthResponse | null,
): ComponentHealthRow[] {
  const fromSystem = system?.components ?? [];
  if (fromSystem.length > 0) {
    return fromSystem.map((c) => ({
      name: c.name,
      status: normalizeStatus(c.status),
      detail: c.detail,
      lastChecked: c.last_checked,
    }));
  }
  if (!actuator?.components) return [];
  // Translate Spring actuator shape: { components: { matchbox: { status: "UP" }, ... } }
  return Object.entries(actuator.components).map(([name, body]) => ({
    name,
    status: normalizeStatus(body.status),
  }));
}

function normalizeStatus(s: string): SystemStatus {
  const u = s.toUpperCase();
  if (u === "UP") return "UP";
  if (u === "DOWN") return "DOWN";
  if (u === "DEGRADED" || u === "OUT_OF_SERVICE") return "DEGRADED";
  return "UNKNOWN";
}

/**
 * Format a timestamp as a relative "3 min ago" / "just now" string. Pure
 * (takes a reference `now`) so it's deterministic in tests.
 */
export function relativeTime(iso: string | null, now: Date): string {
  if (!iso) return "--";
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return "--";
  const diffSec = Math.max(0, Math.round((now.getTime() - t) / 1000));
  if (diffSec < 5) return "just now";
  if (diffSec < 60) return `${diffSec}s ago`;
  if (diffSec < 3600) return `${Math.round(diffSec / 60)} min ago`;
  if (diffSec < 86400) return `${Math.round(diffSec / 3600)} hr ago`;
  return `${Math.round(diffSec / 86400)} day ago`;
}

/**
 * Sum counts across a single bucket (any status). Used by the per-window
 * (today/week/month) cards.
 */
export function bucketTotal(b: ThroughputBucket): number {
  let n = 0;
  for (const v of Object.values(b.counts)) {
    if (typeof v === "number") n += v;
  }
  return n;
}
