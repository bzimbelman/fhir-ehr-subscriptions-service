import type {
  DashboardSnapshot,
  MessagesListResponse,
  ObserveSystem,
  ObserveThroughput,
  SubscriptionsHealthResponse,
} from "@/lib/dashboardTypes";

/**
 * Browser-side data layer for the dashboard. Calls only Next.js API routes
 * (which run on the server and inject the bearer token); never fetches the
 * admin API directly.
 *
 * Each upstream call is wrapped so one failed call doesn't cascade into a
 * blank page. The dashboard renders best-effort with the data it has and
 * surfaces per-section error messages for the rest.
 *
 * The component-health rows on the dashboard come from
 * `observe/system.components` -- an extension point on the observe surface.
 * If the deployed backend predates that field, the component-health section
 * renders an "unknown" placeholder rather than blocking the page. Fetching
 * `/actuator/health/readiness` from the UI would require its own proxy
 * (since the /api/admin/[...path] proxy is rooted at /admin/) -- deferred
 * to a follow-up.
 */

const WINDOWS = {
  today: "24h",
  week: "7d",
  month: "30d",
} as const;

export interface DashboardSnapshotWithTotals extends DashboardSnapshot {
  totalsToday: number;
  totalsWeek: number;
  totalsMonth: number;
  successRateToday: number | null;
  throughputWeek: ObserveThroughput | null;
  throughputMonth: ObserveThroughput | null;
}

interface FetchOptions {
  signal?: AbortSignal;
}

async function safeFetch<T>(
  url: string,
  opts: FetchOptions = {},
): Promise<{ data: T | null; error: string | null }> {
  try {
    const res = await fetch(url, {
      method: "GET",
      headers: { Accept: "application/json" },
      cache: "no-store",
      signal: opts.signal,
    });
    if (!res.ok) {
      return { data: null, error: `${res.status} ${res.statusText}` };
    }
    const body = (await res.json()) as T;
    return { data: body, error: null };
  } catch (e) {
    if ((e as Error).name === "AbortError") {
      return { data: null, error: "aborted" };
    }
    return { data: null, error: (e as Error).message };
  }
}

/**
 * Snapshot the entire dashboard in parallel. Returns a snapshot even when
 * several calls fail -- consumers must check the per-field error strings.
 */
export async function fetchDashboardSnapshot(
  opts: FetchOptions = {},
): Promise<DashboardSnapshotWithTotals> {
  const [
    systemRes,
    throughputTodayRes,
    throughputWeekRes,
    throughputMonthRes,
    messagesRes,
    subsRes,
  ] = await Promise.all([
    safeFetch<ObserveSystem>("/api/admin/observe/system", opts),
    safeFetch<ObserveThroughput>(
      `/api/admin/observe/throughput?window=${WINDOWS.today}`,
      opts,
    ),
    safeFetch<ObserveThroughput>(
      `/api/admin/observe/throughput?window=${WINDOWS.week}`,
      opts,
    ),
    safeFetch<ObserveThroughput>(
      `/api/admin/observe/throughput?window=${WINDOWS.month}`,
      opts,
    ),
    safeFetch<MessagesListResponse>(
      "/api/admin/messages?limit=10&offset=0",
      opts,
    ),
    safeFetch<SubscriptionsHealthResponse>(
      "/api/admin/subscriptions/health",
      opts,
    ),
  ]);

  return {
    system: systemRes.data,
    systemError: systemRes.error,
    throughput: throughputTodayRes.data,
    throughputError: throughputTodayRes.error,
    throughputWeek: throughputWeekRes.data,
    throughputMonth: throughputMonthRes.data,
    recentMessages: messagesRes.data?.items ?? [],
    recentError: messagesRes.error,
    subscriptionsHealth: subsRes.data,
    subscriptionsError: subsRes.error,
    actuator: null,
    actuatorError: null,
    fetchedAt: new Date().toISOString(),
    totalsToday: sumTotals(throughputTodayRes.data),
    totalsWeek: sumTotals(throughputWeekRes.data),
    totalsMonth: sumTotals(throughputMonthRes.data),
    successRateToday: computeSuccessRate(throughputTodayRes.data),
  };
}

function sumTotals(t: ObserveThroughput | null): number {
  if (!t) return 0;
  let n = 0;
  for (const b of t.buckets) {
    for (const v of Object.values(b.counts)) {
      if (typeof v === "number") n += v;
    }
  }
  return n;
}

function computeSuccessRate(t: ObserveThroughput | null): number | null {
  if (!t) return null;
  let delivered = 0;
  let total = 0;
  for (const b of t.buckets) {
    for (const [status, v] of Object.entries(b.counts)) {
      if (typeof v !== "number") continue;
      total += v;
      if (status === "DELIVERED") delivered += v;
    }
  }
  if (total === 0) return null;
  return delivered / total;
}
