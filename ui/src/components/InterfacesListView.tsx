"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import Link from "next/link";
import { InterfaceStatusPill } from "@/components/InterfaceStatusPill";
import { relativeTime } from "@/lib/dashboardMetrics";
import {
  aggregateInterfaces,
  type InterfaceRow,
} from "@/lib/interfaces";
import {
  fetchRecentMessages,
  type FetchResult,
} from "@/lib/interfacesClient";
import type { MessagesListResponse } from "@/lib/dashboardTypes";

/**
 * Top-level list of interfaces (ticket #401).
 *
 * Discovery strategy: pull the most-recent N messages (default 500) and
 * aggregate `(source_system, source_protocol)` client-side. This is fine
 * at our current scale; past a few thousand messages we'd want a
 * `GET /admin/interfaces` summary endpoint server-side. See
 * `lib/interfaces.ts` for the longer note + follow-up TODO.
 */

const DEFAULT_PAGE_SIZE = 500;

interface InterfacesListViewProps {
  /** Test seam -- production passes nothing and the default fetcher is used. */
  fetchMessages?: (
    limit?: number,
    opts?: { signal?: AbortSignal },
  ) => Promise<FetchResult<MessagesListResponse>>;
  /** Test seam: deterministic "now" so the heuristics are stable. */
  nowProvider?: () => Date;
}

export function InterfacesListView({
  fetchMessages = fetchRecentMessages,
  nowProvider,
}: InterfacesListViewProps) {
  const [rows, setRows] = useState<InterfaceRow[] | null>(null);
  const [loading, setLoading] = useState<boolean>(true);
  const [error, setError] = useState<string | null>(null);
  const [scanned, setScanned] = useState<number>(0);
  const [total, setTotal] = useState<number>(0);
  const abortRef = useRef<AbortController | null>(null);

  const refresh = useCallback(async () => {
    abortRef.current?.abort();
    const ctrl = new AbortController();
    abortRef.current = ctrl;
    setLoading(true);
    const res = await fetchMessages(DEFAULT_PAGE_SIZE, { signal: ctrl.signal });
    if (ctrl.signal.aborted) return;
    if (res.error || !res.data) {
      setError(res.error ?? "no data");
      setRows([]);
      setLoading(false);
      return;
    }
    const now = nowProvider ? nowProvider() : new Date();
    setRows(aggregateInterfaces(res.data.items, now));
    setScanned(res.data.items.length);
    setTotal(res.data.total);
    setError(null);
    setLoading(false);
  }, [fetchMessages, nowProvider]);

  useEffect(() => {
    refresh();
    return () => {
      abortRef.current?.abort();
    };
  }, [refresh]);

  return (
    <div className="space-y-6">
      <header className="-mx-6 -mt-6 mb-2 flex flex-wrap items-center justify-between gap-3 border-b border-gray-200 bg-white px-6 py-3">
        <div>
          <h1 className="text-lg font-semibold text-gray-900">Interfaces</h1>
          <p className="text-sm text-gray-500">
            Unique (source system, protocol) pairs that have produced a message.
          </p>
        </div>
        <button
          type="button"
          onClick={refresh}
          disabled={loading}
          data-testid="interfaces-refresh-button"
          className="rounded border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-800 hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-60"
        >
          {loading ? "Refreshing…" : "Refresh"}
        </button>
      </header>

      {error ? (
        <p className="text-sm text-red-700" role="alert">
          Failed to load interfaces: {error}
        </p>
      ) : null}

      <section
        aria-labelledby="interfaces-table-heading"
        className="rounded-lg border border-gray-200 bg-white shadow-sm"
      >
        <div className="flex items-center justify-between border-b border-gray-100 px-4 py-3">
          <h2
            id="interfaces-table-heading"
            className="text-sm font-semibold uppercase tracking-wide text-gray-700"
          >
            Interfaces
          </h2>
          <span className="text-xs text-gray-500" data-testid="scanned-note">
            Aggregated from last {scanned} of {total} messages
          </span>
        </div>
        {rows && rows.length === 0 && !loading ? (
          <p className="px-4 py-6 text-sm text-gray-500">
            No interfaces yet. As soon as a message is received, the
            `(source_system, source_protocol)` pair will appear here.
          </p>
        ) : (
          <table
            className="w-full text-sm"
            data-testid="interfaces-table"
            aria-label="Interfaces"
          >
            <thead>
              <tr className="text-left text-xs font-medium uppercase tracking-wide text-gray-500">
                <th className="px-4 py-2">Interface</th>
                <th className="px-4 py-2">Status</th>
                <th className="px-4 py-2">Last received</th>
                <th className="px-4 py-2 text-right tabular-nums">Today</th>
                <th className="px-4 py-2 text-right tabular-nums">
                  Success today
                </th>
                <th className="px-4 py-2 text-right tabular-nums">Total seen</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {(rows ?? []).map((row) => (
                <tr
                  key={row.slug}
                  data-testid={`interface-row-${row.slug}`}
                  className="hover:bg-gray-50"
                >
                  <td className="px-4 py-2">
                    <Link
                      href={`/interfaces/${row.slug}`}
                      className="font-medium text-blue-700 hover:underline"
                    >
                      {row.name}
                    </Link>
                  </td>
                  <td className="px-4 py-2">
                    <InterfaceStatusPill status={row.status} size="sm" />
                  </td>
                  <td className="px-4 py-2 text-gray-700">
                    {relativeTime(row.lastReceivedAt, nowProvider?.() ?? new Date())}
                  </td>
                  <td className="px-4 py-2 text-right tabular-nums">
                    {row.todayCount}
                  </td>
                  <td className="px-4 py-2 text-right tabular-nums">
                    {row.successRateToday === null
                      ? "--"
                      : `${(row.successRateToday * 100).toFixed(1)}%`}
                  </td>
                  <td className="px-4 py-2 text-right tabular-nums text-gray-500">
                    {row.totalCount}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </div>
  );
}
