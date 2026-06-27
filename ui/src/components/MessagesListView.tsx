"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import { MessageStatusPill } from "@/components/MessageStatusPill";
import { relativeTime } from "@/lib/dashboardMetrics";
import { fetchMessages as defaultFetchMessages } from "@/lib/messagesClient";
import {
  DEFAULT_LIST_FILTERS,
  STATUS_OPTIONS,
  TIME_RANGES,
  type MessagesListFilters,
  type StatusOption,
  type TimeRange,
} from "@/lib/messagesTypes";
import { applyClientFilters, truncateError } from "@/lib/messagesUtils";
import type { MessagesListResponse } from "@/lib/dashboardTypes";

/**
 * Message browser list view (Epic #398, ticket #402).
 *
 * Mirth's message browser is a three-pane filter + list + detail layout;
 * we land the same shape with the detail moved out to `/messages/{id}`.
 * See docs/ui-design/reference-screens/04-message-browser.md.
 *
 * Server-side vs. client-side filtering split:
 *   - `status` and (logically) `source_system` are server-side params on
 *     `/admin/messages`. We pass `status` to the backend; we keep
 *     `source_system` client-side because the operator usually types a
 *     fragment and the backend's filter is exact-match.
 *   - `message_type` and `time_range` are always client-side. Today the
 *     controller doesn't expose those filters; we filter the fetched
 *     page in memory and document the scaling limitation in the UI.
 *
 * The test seam (`fetchMessagesFn`) lets tests inject a deterministic
 * paginated response without monkey-patching `global.fetch`.
 */

const PAGE_SIZE = 50;

interface MessagesListViewProps {
  fetchMessagesFn?: typeof defaultFetchMessages;
  nowProvider?: () => Date;
}

export function MessagesListView({
  fetchMessagesFn = defaultFetchMessages,
  nowProvider,
}: MessagesListViewProps) {
  const [filters, setFilters] = useState<MessagesListFilters>(
    DEFAULT_LIST_FILTERS,
  );
  const [offset, setOffset] = useState<number>(0);
  const [page, setPage] = useState<MessagesListResponse | null>(null);
  const [loading, setLoading] = useState<boolean>(true);
  const [error, setError] = useState<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);

  const load = useCallback(
    async (
      nextOffset: number,
      nextStatus: StatusOption,
    ): Promise<void> => {
      abortRef.current?.abort();
      const ctrl = new AbortController();
      abortRef.current = ctrl;
      setLoading(true);
      setError(null);
      try {
        const res = await fetchMessagesFn(
          {
            status: nextStatus,
            limit: PAGE_SIZE,
            offset: nextOffset,
          },
          { signal: ctrl.signal },
        );
        if (ctrl.signal.aborted) return;
        setPage(res);
      } catch (e) {
        if (ctrl.signal.aborted) return;
        setError((e as Error).message);
        setPage(null);
      } finally {
        if (!ctrl.signal.aborted) setLoading(false);
      }
    },
    [fetchMessagesFn],
  );

  // Refetch when status or offset changes; client-side filters do NOT
  // trigger a refetch. They narrow the rendered slice.
  useEffect(() => {
    void load(offset, filters.status);
    return () => {
      abortRef.current?.abort();
    };
  }, [load, offset, filters.status]);

  // Capture `now` into a memoized value so the rendered slice doesn't
  // recompute on every render when the time-range filter is active.
  // The reference advances when a new fetch completes (page changes) or
  // when filters change — same granularity as DlqView's `now`.
  const now = useMemo(
    () => (nowProvider ? nowProvider() : new Date()),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [nowProvider, page, filters],
  );

  const rendered = useMemo(() => {
    if (!page) return [];
    return applyClientFilters(page.items, filters, now);
  }, [page, filters, now]);

  const onChangeStatus = (next: StatusOption) => {
    setOffset(0);
    setFilters((prev) => ({ ...prev, status: next }));
  };

  const onPrev = () => {
    if (offset === 0) return;
    setOffset(Math.max(0, offset - PAGE_SIZE));
  };

  const onNext = () => {
    if (!page) return;
    if (page.offset + page.items.length >= page.total) return;
    setOffset(offset + PAGE_SIZE);
  };

  const total = page?.total ?? 0;
  const fromIdx = total === 0 ? 0 : (page?.offset ?? 0) + 1;
  const toIdx = page
    ? Math.min((page.offset ?? 0) + page.items.length, total)
    : 0;

  return (
    <div className="space-y-4" data-testid="messages-list-view">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">Messages</h1>
          <p className="text-sm text-gray-600">
            Browse every message that flowed through the engine. Click an id
            to inspect the raw payload, downstream effects, and timeline.
          </p>
        </div>
        <button
          type="button"
          onClick={() => void load(offset, filters.status)}
          disabled={loading}
          data-testid="messages-refresh"
          className="rounded border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-800 hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-60"
        >
          {loading ? "Refreshing…" : "Refresh"}
        </button>
      </header>

      {error ? (
        <p role="alert" className="text-sm text-red-700">
          Failed to load messages: {error}
        </p>
      ) : null}

      <section
        data-testid="messages-filter-bar"
        className="grid grid-cols-1 gap-3 rounded-lg border border-gray-200 bg-white p-3 shadow-sm sm:grid-cols-4"
      >
        <label className="text-xs text-gray-600">
          Status
          <select
            data-testid="messages-filter-status"
            aria-label="Status filter"
            value={filters.status}
            onChange={(e) => onChangeStatus(e.target.value as StatusOption)}
            className="mt-1 block w-full rounded border border-gray-300 bg-white px-2 py-1.5 text-sm"
          >
            {STATUS_OPTIONS.map((s) => (
              <option key={s} value={s}>
                {s === "ALL" ? "All statuses" : s}
              </option>
            ))}
          </select>
        </label>

        <label className="text-xs text-gray-600">
          Source system contains
          <input
            type="text"
            data-testid="messages-filter-source"
            aria-label="Source system contains"
            value={filters.sourceSystem}
            onChange={(e) =>
              setFilters((prev) => ({ ...prev, sourceSystem: e.target.value }))
            }
            placeholder="EPIC"
            className="mt-1 block w-full rounded border border-gray-300 bg-white px-2 py-1.5 text-sm"
          />
        </label>

        <label className="text-xs text-gray-600">
          Message type contains
          <input
            type="text"
            data-testid="messages-filter-type"
            aria-label="Message type contains"
            value={filters.messageType}
            onChange={(e) =>
              setFilters((prev) => ({ ...prev, messageType: e.target.value }))
            }
            placeholder="ADT"
            className="mt-1 block w-full rounded border border-gray-300 bg-white px-2 py-1.5 text-sm"
          />
        </label>

        <label className="text-xs text-gray-600">
          Time range
          <select
            data-testid="messages-filter-time"
            aria-label="Time range"
            value={filters.timeRange}
            onChange={(e) =>
              setFilters((prev) => ({
                ...prev,
                timeRange: e.target.value as TimeRange,
              }))
            }
            className="mt-1 block w-full rounded border border-gray-300 bg-white px-2 py-1.5 text-sm"
          >
            <option value="today">Today</option>
            <option value="24h">Last 24h</option>
            <option value="7d">Last 7 days</option>
            <option value="all">All time</option>
          </select>
        </label>

        <p className="col-span-full text-[11px] text-gray-500">
          Source-system / message-type / time-range filters narrow the
          fetched page in memory. The backend currently exposes only{" "}
          <code className="rounded bg-gray-100 px-1">status</code> and{" "}
          <code className="rounded bg-gray-100 px-1">source_system</code> as
          server-side params; scaling beyond a 50-row page is a follow-up
          (TODO: add{" "}
          <code className="rounded bg-gray-100 px-1">received_after</code> +
          message-type matchers to <code>/admin/messages</code>).{" "}
          <em>Time ranges supported: {TIME_RANGES.join(", ")}.</em>
        </p>
      </section>

      <section className="overflow-hidden rounded-lg border border-gray-200 bg-white shadow-sm">
        <table className="min-w-full text-sm" data-testid="messages-table">
          <thead className="bg-gray-50 text-left text-xs uppercase tracking-wide text-gray-600">
            <tr>
              <th className="px-3 py-2">Status</th>
              <th className="px-3 py-2">ID</th>
              <th className="px-3 py-2">Source</th>
              <th className="px-3 py-2">Type</th>
              <th className="px-3 py-2">Received</th>
              <th className="px-3 py-2 text-right">Attempts</th>
              <th className="px-3 py-2">Last error</th>
            </tr>
          </thead>
          <tbody data-testid="messages-tbody">
            {rendered.length === 0 ? (
              <tr>
                <td
                  colSpan={7}
                  className="px-4 py-6 text-center text-sm text-gray-500"
                >
                  {page && page.items.length === 0
                    ? "No messages match the current status filter."
                    : "Filters hide every loaded row. Reset to see them."}
                </td>
              </tr>
            ) : (
              rendered.map((m) => (
                <tr
                  key={m.id}
                  data-testid={`messages-row-${m.id}`}
                  className="border-t border-gray-100 hover:bg-gray-50"
                >
                  <td className="px-3 py-2 align-top">
                    <MessageStatusPill status={m.status} />
                  </td>
                  <td className="px-3 py-2 align-top font-mono text-xs">
                    <Link
                      href={`/messages/${m.id}`}
                      data-testid={`messages-link-${m.id}`}
                      className="text-blue-700 hover:underline"
                    >
                      {m.id}
                    </Link>
                  </td>
                  <td className="px-3 py-2 align-top">
                    <div className="flex flex-col">
                      <span className="text-sm text-gray-900">
                        {m.source_system}
                      </span>
                      <span className="text-xs text-gray-500">
                        {m.source_protocol}
                      </span>
                    </div>
                  </td>
                  <td className="px-3 py-2 align-top text-gray-800">
                    {m.message_type}
                  </td>
                  <td
                    className="px-3 py-2 align-top text-xs text-gray-700"
                    title={m.received_at ?? ""}
                  >
                    {relativeTime(m.received_at, now)}
                  </td>
                  <td
                    className={
                      "px-3 py-2 align-top text-right text-xs tabular-nums " +
                      (m.attempt_count > 1
                        ? "font-semibold text-amber-700"
                        : "text-gray-800")
                    }
                    data-testid={`messages-attempts-${m.id}`}
                  >
                    {m.attempt_count}
                  </td>
                  <td
                    className="max-w-[28rem] px-3 py-2 align-top text-xs text-gray-700"
                    title={m.last_error ?? ""}
                  >
                    <span className="block truncate font-mono">
                      {truncateError(m.last_error)}
                    </span>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </section>

      <footer className="flex items-center justify-between text-xs text-gray-600">
        <span data-testid="messages-page-summary">
          Showing rows {fromIdx}-{toIdx} of {total}
          {rendered.length !== (page?.items.length ?? 0)
            ? ` (${rendered.length} after filters)`
            : ""}
        </span>
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={onPrev}
            disabled={offset === 0 || loading}
            data-testid="messages-page-prev"
            className="rounded border border-gray-300 bg-white px-2 py-1 hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-50"
          >
            Previous
          </button>
          <span data-testid="messages-page-indicator" className="px-2">
            Page {Math.floor(offset / PAGE_SIZE) + 1}
          </span>
          <button
            type="button"
            onClick={onNext}
            disabled={
              loading ||
              !page ||
              page.offset + page.items.length >= page.total
            }
            data-testid="messages-page-next"
            className="rounded border border-gray-300 bg-white px-2 py-1 hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-50"
          >
            Next
          </button>
        </div>
      </footer>
    </div>
  );
}
