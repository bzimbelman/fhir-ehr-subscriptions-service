"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import { InterfaceStatusPill } from "@/components/InterfaceStatusPill";
import { Sparkline } from "@/components/Sparkline";
import { relativeTime } from "@/lib/dashboardMetrics";
import {
  interfaceStatus,
  throughputToSparkline,
} from "@/lib/interfaces";
import {
  fetchInterfaceMessages,
  fetchThroughput24h,
  type FetchResult,
} from "@/lib/interfacesClient";
import type {
  MessageStatus,
  MessagesListResponse,
  ObserveThroughput,
} from "@/lib/dashboardTypes";

/**
 * Drill-down for a single interface (ticket #401).
 *
 * Sections (top -> bottom):
 *   1. Header card: name + status pill + last-message-at
 *   2. Sparkline of messages-per-hour over the last 24h
 *      (today the throughput endpoint isn't per-source-system; documented gap)
 *   3. Filters: status dropdown, message-type contains filter, page size note
 *   4. Recent messages table (paginated, 50/page)
 *
 * Filters update local state -- the status filter additionally triggers a
 * fresh fetch with `?status=X` so the table actually reflects the chosen
 * status (rather than just hiding rows from a stale page).
 */

const STATUS_OPTIONS: ("ALL" | MessageStatus)[] = [
  "ALL",
  "RECEIVED",
  "TRANSFORMING",
  "DELIVERED",
  "FAILED",
  "DEAD_LETTER",
];

const STATUS_TONE: Record<MessageStatus, string> = {
  RECEIVED: "bg-blue-100 text-blue-800 ring-blue-300",
  TRANSFORMING: "bg-indigo-100 text-indigo-800 ring-indigo-300",
  DELIVERED: "bg-green-100 text-green-800 ring-green-300",
  FAILED: "bg-yellow-100 text-yellow-800 ring-yellow-300",
  DEAD_LETTER: "bg-red-100 text-red-800 ring-red-300",
};

interface InterfaceDetailViewProps {
  sourceSystem: string;
  sourceProtocol: string;
  /** Test seam: deterministic "now". */
  nowProvider?: () => Date;
  /** Test seam for the messages fetcher. */
  fetchMessages?: typeof fetchInterfaceMessages;
  /** Test seam for the throughput fetcher. */
  fetchThroughput?: typeof fetchThroughput24h;
}

const PAGE_SIZE = 50;

export function InterfaceDetailView({
  sourceSystem,
  sourceProtocol,
  nowProvider,
  fetchMessages = fetchInterfaceMessages,
  fetchThroughput = fetchThroughput24h,
}: InterfaceDetailViewProps) {
  const [statusFilter, setStatusFilter] = useState<"ALL" | MessageStatus>(
    "ALL",
  );
  const [typeFilter, setTypeFilter] = useState<string>("");
  const [offset, setOffset] = useState<number>(0);
  const [messages, setMessages] = useState<MessagesListResponse | null>(null);
  const [messagesError, setMessagesError] = useState<string | null>(null);
  const [throughput, setThroughput] = useState<ObserveThroughput | null>(null);
  const [throughputError, setThroughputError] = useState<string | null>(null);
  const [loading, setLoading] = useState<boolean>(true);
  const abortRef = useRef<AbortController | null>(null);

  const refresh = useCallback(async () => {
    abortRef.current?.abort();
    const ctrl = new AbortController();
    abortRef.current = ctrl;
    setLoading(true);

    const [msgRes, tpRes]: [
      FetchResult<MessagesListResponse>,
      FetchResult<ObserveThroughput>,
    ] = await Promise.all([
      fetchMessages(
        {
          sourceSystem,
          status: statusFilter === "ALL" ? undefined : statusFilter,
          limit: PAGE_SIZE,
          offset,
        },
        { signal: ctrl.signal },
      ),
      fetchThroughput({ signal: ctrl.signal }),
    ]);

    if (ctrl.signal.aborted) return;
    setMessages(msgRes.data);
    setMessagesError(msgRes.error);
    setThroughput(tpRes.data);
    setThroughputError(tpRes.error);
    setLoading(false);
  }, [fetchMessages, fetchThroughput, sourceSystem, statusFilter, offset]);

  useEffect(() => {
    refresh();
    return () => {
      abortRef.current?.abort();
    };
  }, [refresh]);

  const sparklinePoints = useMemo(
    () => throughputToSparkline(throughput),
    [throughput],
  );

  // Status pill: compute from the current page's messages -- if the user
  // is staring at the most-recent page it's accurate; if they paged back
  // it's "status of the displayed window" which is still useful for triage.
  const now = nowProvider ? nowProvider() : new Date();
  const status = interfaceStatus(messages?.items ?? [], now);
  const lastReceivedAt = messages?.items[0]?.received_at ?? null;

  const filtered = useMemo(() => {
    if (!messages) return [];
    if (!typeFilter.trim()) return messages.items;
    const needle = typeFilter.toLowerCase();
    return messages.items.filter((m) =>
      m.message_type.toLowerCase().includes(needle),
    );
  }, [messages, typeFilter]);

  return (
    <div className="space-y-6">
      <header className="-mx-6 -mt-6 mb-2 flex flex-wrap items-center justify-between gap-3 border-b border-gray-200 bg-white px-6 py-3">
        <div>
          <div className="text-xs text-gray-500">
            <Link href="/interfaces" className="hover:underline">
              Interfaces
            </Link>{" "}
            /
          </div>
          <h1 className="text-lg font-semibold text-gray-900">
            {sourceSystem} / {sourceProtocol}
          </h1>
          <p className="text-sm text-gray-500" data-testid="last-received">
            Last received {relativeTime(lastReceivedAt, now)}
          </p>
        </div>
        <div className="flex items-center gap-3">
          <InterfaceStatusPill status={status} />
          <button
            type="button"
            onClick={refresh}
            disabled={loading}
            data-testid="detail-refresh-button"
            className="rounded border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-800 hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-60"
          >
            {loading ? "Refreshing…" : "Refresh"}
          </button>
        </div>
      </header>

      <section
        aria-labelledby="throughput-heading"
        className="rounded-lg border border-gray-200 bg-white p-4 shadow-sm"
      >
        <h2
          id="throughput-heading"
          className="mb-3 text-sm font-semibold uppercase tracking-wide text-gray-700"
        >
          Messages per hour (24h)
        </h2>
        {throughputError ? (
          <p className="text-sm text-red-700">
            Failed to load throughput: {throughputError}
          </p>
        ) : (
          <>
            <Sparkline
              points={sparklinePoints}
              label={`Throughput for ${sourceSystem} / ${sourceProtocol}`}
            />
            <p className="mt-2 text-xs text-gray-500">
              Note: backend throughput is not yet scoped by source_system;
              this sparkline reflects ALL interfaces. Per-interface scoping
              lands in a follow-up.
            </p>
          </>
        )}
      </section>

      <section
        aria-labelledby="recent-messages-heading"
        className="rounded-lg border border-gray-200 bg-white shadow-sm"
      >
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-4 py-3">
          <h2
            id="recent-messages-heading"
            className="text-sm font-semibold uppercase tracking-wide text-gray-700"
          >
            Recent messages
          </h2>
          <div className="flex flex-wrap items-center gap-2">
            <label className="text-xs text-gray-600">
              Status
              <select
                aria-label="Status filter"
                data-testid="status-filter"
                value={statusFilter}
                onChange={(e) => {
                  setStatusFilter(e.target.value as "ALL" | MessageStatus);
                  setOffset(0);
                }}
                className="ml-2 rounded border border-gray-300 bg-white px-2 py-1 text-sm"
              >
                {STATUS_OPTIONS.map((s) => (
                  <option key={s} value={s}>
                    {s === "ALL" ? "All" : s}
                  </option>
                ))}
              </select>
            </label>
            <label className="text-xs text-gray-600">
              Type contains
              <input
                type="text"
                aria-label="Message type filter"
                data-testid="message-type-filter"
                value={typeFilter}
                onChange={(e) => setTypeFilter(e.target.value)}
                placeholder="ADT"
                className="ml-2 rounded border border-gray-300 bg-white px-2 py-1 text-sm"
              />
            </label>
          </div>
        </div>

        {messagesError ? (
          <p className="px-4 py-3 text-sm text-red-700">
            Failed to load messages: {messagesError}
          </p>
        ) : null}

        {messages && filtered.length === 0 && !loading ? (
          <p className="px-4 py-6 text-sm text-gray-500">
            No messages match the current filters.
          </p>
        ) : (
          <table
            className="w-full text-sm"
            data-testid="messages-table"
            aria-label="Recent messages"
          >
            <thead>
              <tr className="text-left text-xs font-medium uppercase tracking-wide text-gray-500">
                <th className="px-4 py-2">Status</th>
                <th className="px-4 py-2">Source ID</th>
                <th className="px-4 py-2">Type</th>
                <th className="px-4 py-2">Received</th>
                <th className="px-4 py-2 text-right tabular-nums">Attempts</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {filtered.map((m) => (
                <tr
                  key={m.id}
                  data-testid={`message-row-${m.id}`}
                  className="hover:bg-gray-50"
                >
                  <td className="px-4 py-2">
                    <Link
                      href={`/messages/${m.id}`}
                      data-testid={`message-link-${m.id}`}
                      className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset ${STATUS_TONE[m.status]} hover:underline`}
                    >
                      {m.status}
                    </Link>
                  </td>
                  <td
                    className="px-4 py-2 font-mono text-xs text-gray-700"
                    title={m.source_id}
                  >
                    {m.source_id}
                  </td>
                  <td className="px-4 py-2">{m.message_type}</td>
                  <td className="px-4 py-2 text-gray-700">
                    {relativeTime(m.received_at, now)}
                  </td>
                  <td className="px-4 py-2 text-right tabular-nums">
                    {m.attempt_count}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}

        {messages ? (
          <div className="flex items-center justify-between border-t border-gray-100 px-4 py-2 text-xs text-gray-600">
            <span data-testid="pagination-summary">
              Showing {messages.offset + 1}–
              {Math.min(messages.offset + messages.items.length, messages.total)}{" "}
              of {messages.total}
            </span>
            <div className="flex gap-2">
              <button
                type="button"
                onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
                disabled={offset === 0 || loading}
                data-testid="page-prev"
                className="rounded border border-gray-300 bg-white px-2 py-1 disabled:cursor-not-allowed disabled:opacity-50"
              >
                Prev
              </button>
              <button
                type="button"
                onClick={() => setOffset(offset + PAGE_SIZE)}
                disabled={
                  loading ||
                  messages.offset + messages.items.length >= messages.total
                }
                data-testid="page-next"
                className="rounded border border-gray-300 bg-white px-2 py-1 disabled:cursor-not-allowed disabled:opacity-50"
              >
                Next
              </button>
            </div>
          </div>
        ) : null}
      </section>
    </div>
  );
}
