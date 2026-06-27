import type {
  AuditFilters,
  AuditSearchResponse,
} from "@/lib/auditTypes";

/**
 * Browser-side data layer for the audit log view (Epic #398, ticket #407).
 *
 * Calls the Next.js API route at `/api/admin/audit*` -- the bearer token
 * never reaches the browser (same model as #400, #404, #405).
 *
 * Each function returns a `{data, error}` envelope rather than throwing
 * so the audit view renders a per-section error without blanking.
 */

export interface ApiResult<T> {
  data: T | null;
  error: string | null;
}

interface FetchOptions {
  signal?: AbortSignal;
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
 * Build the query string for /api/admin/audit from filters + pagination.
 * Empty / undefined filter values are omitted so the URL stays tidy.
 * Exported for testability (the audit view test asserts the URL shape).
 */
export function buildAuditQueryString(
  filters: AuditFilters,
  limit: number,
  offset: number,
): string {
  const params = new URLSearchParams();
  if (filters.type) params.set("type", filters.type);
  if (filters.subtype) params.set("subtype", filters.subtype);
  if (filters.outcome) params.set("outcome", filters.outcome);
  if (filters.agent) params.set("agent", filters.agent);
  if (filters.dateFrom) params.set("date-from", filters.dateFrom);
  if (filters.dateTo) params.set("date-to", filters.dateTo);
  params.set("limit", String(limit));
  params.set("offset", String(offset));
  return params.toString();
}

export async function fetchAuditEvents(
  filters: AuditFilters,
  limit: number,
  offset: number,
  opts: FetchOptions = {},
): Promise<ApiResult<AuditSearchResponse>> {
  const qs = buildAuditQueryString(filters, limit, offset);
  return safeFetch<AuditSearchResponse>(`/api/admin/audit?${qs}`, opts);
}

export async function fetchAuditEvent(
  id: string,
  opts: FetchOptions = {},
): Promise<ApiResult<unknown>> {
  // id is "AuditEvent/abc" or just "abc"; we want the bare id on the
  // URL so the path matches `/admin/audit/{id}`.
  const bareId = id.includes("/") ? id.split("/").slice(-1)[0]! : id;
  return safeFetch<unknown>(
    `/api/admin/audit/${encodeURIComponent(bareId)}`,
    opts,
  );
}
