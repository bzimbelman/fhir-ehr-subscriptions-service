import type {
  MessagesListResponse,
  MessageStatus,
} from "@/lib/dashboardTypes";
import type {
  MessageDetailRow,
  MessageEffectsResponse,
} from "@/lib/messagesTypes";

/**
 * Browser-side data layer for the message viewer (Epic #398, ticket #402).
 *
 * All calls go through the Next.js `/api/admin/[...path]` proxy so the
 * admin bearer token never reaches the browser. The proxy adds an audit
 * breadcrumb for POST / DELETE.
 *
 * We deliberately do NOT reuse `dlqClient.ts` even though the URLs
 * overlap — the DLQ page treats the message store as DEAD_LETTER-only and
 * issues bulk actions; this page treats it as the general store and
 * issues single-row actions. Keeping the modules separate prevents one
 * page's refactor from breaking the other.
 *
 * Function shape: each request returns either the parsed body OR throws
 * with a short message. The view layer is responsible for try/catch +
 * surfacing the error to the operator (matches the InterfaceDetailView /
 * SubscriptionDetail pattern).
 */

interface FetchOpts {
  signal?: AbortSignal;
}

export interface MessagesListQuery {
  /** Backend `status` filter — pass undefined / "ALL" for "no filter". */
  status?: MessageStatus | "ALL";
  /** Backend `source_system` filter — pass undefined / "" for "no filter". */
  sourceSystem?: string;
  limit?: number;
  offset?: number;
}

/**
 * List a page of messages. Maps directly onto the controller's query
 * params; pagination defaults to 50 rows starting at offset 0.
 */
export async function fetchMessages(
  q: MessagesListQuery = {},
  opts: FetchOpts = {},
): Promise<MessagesListResponse> {
  const params = new URLSearchParams();
  if (q.status && q.status !== "ALL") params.set("status", q.status);
  if (q.sourceSystem && q.sourceSystem.trim().length > 0) {
    params.set("source_system", q.sourceSystem.trim());
  }
  params.set("limit", String(q.limit ?? 50));
  params.set("offset", String(q.offset ?? 0));

  const url = `/api/admin/messages?${params.toString()}`;
  const res = await fetch(url, {
    method: "GET",
    headers: { Accept: "application/json" },
    cache: "no-store",
    signal: opts.signal,
  });
  if (!res.ok) {
    throw new Error(`Failed to fetch messages (${res.status} ${res.statusText})`);
  }
  return (await res.json()) as MessagesListResponse;
}

/** Fetch the full detail (including `raw_message`) for one row. */
export async function fetchMessageDetail(
  id: number | string,
  opts: FetchOpts = {},
): Promise<MessageDetailRow> {
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
  return (await res.json()) as MessageDetailRow;
}

/**
 * Fetch the `effects` projection for one message — FHIR resources created
 * and subscriptions/notifications that fired. Backed by
 * `AdminMessageEffectsController` (Epic #387, ticket #392).
 */
export async function fetchMessageEffects(
  id: number | string,
  opts: FetchOpts = {},
): Promise<MessageEffectsResponse> {
  const res = await fetch(`/api/admin/messages/${id}/effects`, {
    method: "GET",
    headers: { Accept: "application/json" },
    cache: "no-store",
    signal: opts.signal,
  });
  if (!res.ok) {
    throw new Error(
      `Failed to fetch effects for ${id} (${res.status} ${res.statusText})`,
    );
  }
  return (await res.json()) as MessageEffectsResponse;
}

/**
 * Retry a single message. Backend 409s if the row isn't in FAILED or
 * DEAD_LETTER; we surface the upstream status verbatim so the operator
 * can see why it was rejected.
 */
export async function retryMessage(id: number | string): Promise<void> {
  const res = await fetch(`/api/admin/messages/${id}/retry`, {
    method: "POST",
    headers: {
      Accept: "application/json",
      "Content-Type": "application/json",
    },
    cache: "no-store",
  });
  if (!res.ok) {
    const text = await safeText(res);
    throw new Error(
      `Retry failed for ${id} (${res.status} ${res.statusText})${text ? `: ${text}` : ""}`,
    );
  }
}

/** Delete a single message. Backend 409s if the row isn't DEAD_LETTER. */
export async function deleteMessage(id: number | string): Promise<void> {
  const res = await fetch(`/api/admin/messages/${id}`, {
    method: "DELETE",
    headers: { Accept: "application/json" },
    cache: "no-store",
  });
  if (!res.ok) {
    const text = await safeText(res);
    throw new Error(
      `Delete failed for ${id} (${res.status} ${res.statusText})${text ? `: ${text}` : ""}`,
    );
  }
}

async function safeText(res: Response): Promise<string> {
  try {
    return (await res.text()).slice(0, 240);
  } catch {
    return "";
  }
}
