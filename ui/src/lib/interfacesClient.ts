import type {
  MessagesListResponse,
  ObserveThroughput,
} from "@/lib/dashboardTypes";

/**
 * Browser-side data layer for the /interfaces views (ticket #401). Mirrors
 * the wrapper pattern from `dashboardClient.ts`: each upstream call returns
 * its data + an optional error string, so one failed endpoint doesn't blank
 * the page.
 */

interface FetchOptions {
  signal?: AbortSignal;
}

export interface FetchResult<T> {
  data: T | null;
  error: string | null;
}

async function safeFetch<T>(
  url: string,
  opts: FetchOptions = {},
): Promise<FetchResult<T>> {
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
    return { data: (await res.json()) as T, error: null };
  } catch (e) {
    if ((e as Error).name === "AbortError") {
      return { data: null, error: "aborted" };
    }
    return { data: null, error: (e as Error).message };
  }
}

/**
 * Pull the most-recent slice of messages so the page can aggregate
 * (source_system, source_protocol) client-side. See `lib/interfaces.ts`
 * for the scaling note.
 */
export function fetchRecentMessages(
  limit = 500,
  opts: FetchOptions = {},
): Promise<FetchResult<MessagesListResponse>> {
  return safeFetch<MessagesListResponse>(
    `/api/admin/messages?limit=${limit}&offset=0`,
    opts,
  );
}

export interface InterfaceMessagesQuery {
  sourceSystem: string;
  /** Optional MessageStatus filter -- "" / undefined means all. */
  status?: string;
  limit?: number;
  offset?: number;
}

export function fetchInterfaceMessages(
  q: InterfaceMessagesQuery,
  opts: FetchOptions = {},
): Promise<FetchResult<MessagesListResponse>> {
  const params = new URLSearchParams();
  params.set("source_system", q.sourceSystem);
  if (q.status) params.set("status", q.status);
  params.set("limit", String(q.limit ?? 50));
  params.set("offset", String(q.offset ?? 0));
  return safeFetch<MessagesListResponse>(
    `/api/admin/messages?${params.toString()}`,
    opts,
  );
}

export function fetchThroughput24h(
  opts: FetchOptions = {},
): Promise<FetchResult<ObserveThroughput>> {
  return safeFetch<ObserveThroughput>(
    "/api/admin/observe/throughput?window=24h",
    opts,
  );
}
