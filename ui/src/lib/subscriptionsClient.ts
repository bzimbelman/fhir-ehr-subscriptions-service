import type {
  FhirSubscriptionResource,
  SubscriptionHistoryEnvelope,
  SubscriptionsHealthEnvelope,
} from "@/lib/subscriptionTypes";

/**
 * Browser-side data layer for the `/admin/subscriptions/*` endpoints
 * (Epic #398, ticket #404). Calls only the Next.js API route at
 * `/api/admin/[...path]` — the bearer token never reaches the
 * browser (same model as #400's `dashboardClient`).
 *
 * Each function returns a `{data, error}` envelope rather than
 * throwing, so callers can render per-section error messages without
 * blanking the whole page on a transient upstream failure. This
 * mirrors the pattern established for the dashboard.
 */

interface FetchOptions {
  signal?: AbortSignal;
}

export interface ApiResult<T> {
  data: T | null;
  error: string | null;
}

async function safeFetch<T>(
  url: string,
  init: RequestInit & { signal?: AbortSignal } = {},
): Promise<ApiResult<T>> {
  try {
    const res = await fetch(url, {
      method: "GET",
      ...init,
      headers: {
        Accept: "application/json",
        ...(init.headers ?? {}),
      },
      cache: "no-store",
    });
    if (!res.ok) {
      return { data: null, error: `${res.status} ${res.statusText}` };
    }
    // 204 responses (none today, but defensive) have no body.
    if (res.status === 204) {
      return { data: null, error: null };
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
 * List view: pulls `/admin/subscriptions/health`. The backend returns a
 * `{total, items[]}` envelope — see `SubscriptionsHealthResponse` in
 * `AdminSubscriptionsController.kt`.
 */
export async function fetchSubscriptionsHealth(
  opts: FetchOptions = {},
): Promise<ApiResult<SubscriptionsHealthEnvelope>> {
  return safeFetch<SubscriptionsHealthEnvelope>(
    "/api/admin/subscriptions/health",
    opts,
  );
}

/**
 * Per-subscription history. `id` is the bare HAPI id ("123"), not
 * "Subscription/123" — strip the prefix at the call site if you only
 * have the reference form. The default limit matches the backend's
 * default (50).
 */
export async function fetchSubscriptionHistory(
  id: string,
  opts: FetchOptions & { limit?: number; offset?: number } = {},
): Promise<ApiResult<SubscriptionHistoryEnvelope>> {
  const limit = opts.limit ?? 50;
  const offset = opts.offset ?? 0;
  const url = `/api/admin/subscriptions/${encodeURIComponent(id)}/history?limit=${limit}&offset=${offset}`;
  return safeFetch<SubscriptionHistoryEnvelope>(url, { signal: opts.signal });
}

/**
 * Full FHIR Subscription resource (ticket #404 added this endpoint).
 * Used by the detail page's "Configuration" panel.
 */
export async function fetchSubscriptionResource(
  id: string,
  opts: FetchOptions = {},
): Promise<ApiResult<FhirSubscriptionResource>> {
  return safeFetch<FhirSubscriptionResource>(
    `/api/admin/subscriptions/${encodeURIComponent(id)}/resource`,
    opts,
  );
}

/**
 * Status toggle. Sends a PATCH to `/admin/subscriptions/{id}/status`.
 * The backend audits the action with a structured log line (#407 will
 * replace that with a proper AuditEvent resource).
 */
export async function patchSubscriptionStatus(
  id: string,
  newStatus: "active" | "off" | "requested" | "error",
  opts: FetchOptions = {},
): Promise<ApiResult<unknown>> {
  return safeFetch<unknown>(
    `/api/admin/subscriptions/${encodeURIComponent(id)}/status`,
    {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ status: newStatus }),
      signal: opts.signal,
    },
  );
}

/**
 * Strip "Subscription/" prefix so callers can hit endpoints which want
 * the bare id. Idempotent on already-bare ids.
 */
export function bareSubscriptionId(idOrReference: string): string {
  const slash = idOrReference.indexOf("/");
  if (slash === -1) return idOrReference;
  return idOrReference.slice(slash + 1);
}
