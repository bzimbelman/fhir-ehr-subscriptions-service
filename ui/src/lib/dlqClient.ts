import type {
  MessagesListResponse,
  MessageSummary,
} from "@/lib/dashboardTypes";
import type { BulkActionOutcome, MessageDetail } from "@/lib/dlqTypes";

/**
 * Browser-side data layer for the DLQ viewer (Epic #398, ticket #403).
 *
 * Every request goes through the Next.js `/api/admin/[...path]` proxy. The
 * proxy attaches the server-side bearer token; this module never sees it.
 *
 * Bulk actions issue one POST/DELETE per selected id. We deliberately keep
 * those serial (sequential awaits) for a few reasons:
 *
 *   - The backend's retry endpoint mutates a row each time. Parallelism gives
 *     no real wall-clock benefit for typical 1-50 row selections, and a burst
 *     of concurrent writes adds DB contention for no UI benefit.
 *   - We want per-row outcomes for the operator: "5 succeeded, 1 failed
 *     (409)". Serial calls let us assemble a clean per-id outcome list.
 *
 * Tests stub `fetch` directly; this module has no other side-effects.
 */

interface FetchOpts {
  signal?: AbortSignal;
}

/**
 * Fetch a page of DEAD_LETTER messages from the admin API. `limit` defaults
 * to the page size we use in the UI (50). The backend already filters by
 * status; client-side filters narrow what's actually rendered.
 */
export async function fetchDlqPage(
  opts: { limit?: number; offset?: number } & FetchOpts = {},
): Promise<MessagesListResponse> {
  const limit = opts.limit ?? 50;
  const offset = opts.offset ?? 0;
  const url = `/api/admin/messages?status=DEAD_LETTER&limit=${limit}&offset=${offset}`;
  const res = await fetch(url, {
    method: "GET",
    headers: { Accept: "application/json" },
    cache: "no-store",
    signal: opts.signal,
  });
  if (!res.ok) {
    throw new Error(`Failed to fetch DLQ (${res.status} ${res.statusText})`);
  }
  return (await res.json()) as MessagesListResponse;
}

/** Fetch the full detail (incl. raw_message) for a single DLQ row. */
export async function fetchMessageDetail(
  id: number,
  opts: FetchOpts = {},
): Promise<MessageDetail> {
  const res = await fetch(`/api/admin/messages/${id}`, {
    method: "GET",
    headers: { Accept: "application/json" },
    cache: "no-store",
    signal: opts.signal,
  });
  if (!res.ok) {
    throw new Error(
      `Failed to fetch message ${id} (${res.status} ${res.statusText})`,
    );
  }
  return (await res.json()) as MessageDetail;
}

/**
 * Issue a single retry. The proxy logs the action as an audit breadcrumb;
 * we still log on the browser side too so a Chrome devtools session shows
 * what the operator did.
 */
export async function retryMessage(id: number): Promise<BulkActionOutcome> {
  try {
    const res = await fetch(`/api/admin/messages/${id}/retry`, {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      cache: "no-store",
    });
    if (!res.ok) {
      const body = await safeReadText(res);
      return {
        id,
        ok: false,
        status: res.status,
        error: body || `${res.status} ${res.statusText}`,
      };
    }
    return { id, ok: true, status: res.status };
  } catch (e) {
    return { id, ok: false, error: (e as Error).message };
  }
}

/** Issue a single delete. Backend 409s if the row isn't in DEAD_LETTER. */
export async function deleteMessage(id: number): Promise<BulkActionOutcome> {
  try {
    const res = await fetch(`/api/admin/messages/${id}`, {
      method: "DELETE",
      headers: { Accept: "application/json" },
      cache: "no-store",
    });
    if (!res.ok) {
      const body = await safeReadText(res);
      return {
        id,
        ok: false,
        status: res.status,
        error: body || `${res.status} ${res.statusText}`,
      };
    }
    return { id, ok: true, status: res.status };
  } catch (e) {
    return { id, ok: false, error: (e as Error).message };
  }
}

/**
 * Bulk replay -- serial. Returns one outcome per id in the same order.
 * Callers can show per-row pills as the outcomes come back.
 */
export async function bulkRetry(
  ids: readonly number[],
): Promise<BulkActionOutcome[]> {
  const out: BulkActionOutcome[] = [];
  for (const id of ids) {
    out.push(await retryMessage(id));
  }
  return out;
}

/** Bulk delete -- serial; see bulkRetry rationale. */
export async function bulkDelete(
  ids: readonly number[],
): Promise<BulkActionOutcome[]> {
  const out: BulkActionOutcome[] = [];
  for (const id of ids) {
    out.push(await deleteMessage(id));
  }
  return out;
}

async function safeReadText(res: Response): Promise<string> {
  try {
    const t = await res.text();
    return t.slice(0, 240); // bound the size in case of HTML error pages
  } catch {
    return "";
  }
}

/** Re-export a small alias used by the view for clearer code. */
export type DlqRow = MessageSummary;
