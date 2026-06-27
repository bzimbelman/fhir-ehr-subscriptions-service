"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import type { MessageSummary } from "@/lib/dashboardTypes";
import type {
  BulkActionOutcome,
  DlqFilters as DlqFiltersState,
  MessageDetail,
} from "@/lib/dlqTypes";
import { DEFAULT_FILTERS } from "@/lib/dlqTypes";
import {
  bulkDelete as defaultBulkDelete,
  bulkRetry as defaultBulkRetry,
  fetchDlqPage as defaultFetchDlqPage,
  fetchMessageDetail as defaultFetchDetail,
} from "@/lib/dlqClient";
import { groupByFingerprint, matchesFilters } from "@/lib/dlqUtils";
import { CommonErrorsPanel } from "@/components/CommonErrorsPanel";
import { ConfirmDeleteModal } from "@/components/ConfirmDeleteModal";
import { DlqFilters } from "@/components/DlqFilters";
import { DlqRow } from "@/components/DlqRow";

/**
 * Main client view for /dlq (Epic #398, ticket #403).
 *
 * Page layout (top to bottom):
 *   1. Heading + page-level error banner
 *   2. Common errors panel (top-5 fingerprints over the loaded page)
 *   3. Filter bar (controlled state, used to narrow the rendered rows)
 *   4. Bulk-action toolbar: count, "Replay selected", "Discard selected"
 *   5. Table of DEAD_LETTER messages, click row to expand inline
 *   6. Pagination at the bottom (page size 50)
 *
 * Data flow:
 *   - On mount + every "Refresh" click, fetch one page from
 *     /api/admin/messages?status=DEAD_LETTER. We do NOT poll automatically:
 *     the dashboard polls; the DLQ page is an "I'm here to fix things"
 *     workspace and the operator should see deterministic state while
 *     deciding what to replay.
 *   - Filters and the common-errors grouping are computed CLIENT-SIDE over
 *     the fetched page. Backend-side fingerprinting is a follow-up.
 *
 * Test seams: `fetchPage`, `bulkRetryFn`, `bulkDeleteFn`, `fetchDetailFn`,
 * `nowProvider` — production code never sets these; tests inject stubs.
 */
const PAGE_SIZE = 50;

interface DlqViewProps {
  fetchPage?: typeof defaultFetchDlqPage;
  bulkRetryFn?: typeof defaultBulkRetry;
  bulkDeleteFn?: typeof defaultBulkDelete;
  fetchDetailFn?: (id: number) => Promise<MessageDetail>;
  nowProvider?: () => Date;
}

export function DlqView({
  fetchPage = defaultFetchDlqPage,
  bulkRetryFn = defaultBulkRetry,
  bulkDeleteFn = defaultBulkDelete,
  fetchDetailFn = defaultFetchDetail,
  nowProvider,
}: DlqViewProps) {
  const [rows, setRows] = useState<MessageSummary[]>([]);
  const [total, setTotal] = useState<number>(0);
  const [offset, setOffset] = useState<number>(0);
  const [loading, setLoading] = useState<boolean>(true);
  const [error, setError] = useState<string | null>(null);
  const [filters, setFilters] = useState<DlqFiltersState>(DEFAULT_FILTERS);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [outcomes, setOutcomes] = useState<Map<number, BulkActionOutcome>>(
    new Map(),
  );
  const [confirmDeleteOpen, setConfirmDeleteOpen] = useState(false);
  const [acting, setActing] = useState(false);

  // `now` is captured into state on each fetch so the reference is stable
  // across renders that don't change the underlying data. The age-band /
  // time-range filters thus advance in chunks (one chunk per refresh)
  // rather than continuously, which is the right granularity for an
  // operator triaging the DLQ. Tests pin it via nowProvider.
  const initialNow = nowProvider ? nowProvider() : new Date();
  const [now, setNow] = useState<Date>(initialNow);

  const load = useCallback(
    async (nextOffset: number) => {
      setLoading(true);
      try {
        const page = await fetchPage({
          limit: PAGE_SIZE,
          offset: nextOffset,
        });
        setRows(page.items);
        setTotal(page.total);
        setOffset(page.offset);
        setError(null);
        setNow(nowProvider ? nowProvider() : new Date());
      } catch (e) {
        setError((e as Error).message);
      } finally {
        setLoading(false);
      }
    },
    [fetchPage, nowProvider],
  );

  useEffect(() => {
    void load(0);
  }, [load]);

  const filtered = useMemo(
    () => rows.filter((r) => matchesFilters(r, filters, now)),
    [rows, filters, now],
  );

  const fingerprints = useMemo(
    () => groupByFingerprint(filtered, 5),
    [filtered],
  );

  const allSelected = filtered.length > 0 && filtered.every((r) => selected.has(r.id));

  const toggleSelectAll = (checked: boolean) => {
    if (!checked) {
      setSelected(new Set());
      return;
    }
    setSelected(new Set(filtered.map((r) => r.id)));
  };

  const toggleSelectOne = (id: number, on: boolean) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });
  };

  const onPickFingerprint = (fp: string) => {
    setFilters((prev) => ({ ...prev, lastErrorPattern: fp }));
  };

  const onReplay = async () => {
    if (selected.size === 0 || acting) return;
    const ids = Array.from(selected.values());
    setActing(true);
    try {
      const results = await bulkRetryFn(ids);
      setOutcomes((prev) => {
        const next = new Map(prev);
        for (const r of results) next.set(r.id, r);
        return next;
      });
      // Refresh after replay -- retried rows move out of DEAD_LETTER.
      await load(offset);
      // Clear successful selections; keep failed ones so the operator can
      // see what didn't replay and decide what to do.
      setSelected((prev) => {
        const next = new Set(prev);
        for (const r of results) {
          if (r.ok) next.delete(r.id);
        }
        return next;
      });
    } finally {
      setActing(false);
    }
  };

  const onDeleteRequested = () => {
    if (selected.size === 0 || acting) return;
    setConfirmDeleteOpen(true);
  };

  const onDeleteConfirmed = async () => {
    setConfirmDeleteOpen(false);
    if (selected.size === 0 || acting) return;
    const ids = Array.from(selected.values());
    setActing(true);
    try {
      const results = await bulkDeleteFn(ids);
      setOutcomes((prev) => {
        const next = new Map(prev);
        for (const r of results) next.set(r.id, r);
        return next;
      });
      await load(offset);
      setSelected((prev) => {
        const next = new Set(prev);
        for (const r of results) {
          if (r.ok) next.delete(r.id);
        }
        return next;
      });
    } finally {
      setActing(false);
    }
  };

  const onDeleteCancelled = () => setConfirmDeleteOpen(false);

  const onPrev = () => {
    if (offset === 0) return;
    void load(Math.max(0, offset - PAGE_SIZE));
  };
  const onNext = () => {
    if (offset + PAGE_SIZE >= total) return;
    void load(offset + PAGE_SIZE);
  };

  return (
    <div className="space-y-4">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">
            Dead-letter queue
          </h1>
          <p className="text-sm text-gray-600">
            Messages that exhausted retries. Replay to re-queue or discard to
            purge. Every action is recorded in the audit log.
          </p>
        </div>
        <button
          type="button"
          onClick={() => void load(offset)}
          disabled={loading}
          data-testid="dlq-refresh"
          className="rounded border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-800 hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-60"
        >
          {loading ? "Refreshing…" : "Refresh"}
        </button>
      </header>

      {error ? (
        <p role="alert" className="text-sm text-red-700">
          Failed to load DLQ: {error}
        </p>
      ) : null}

      <CommonErrorsPanel
        groups={fingerprints}
        rowCount={filtered.length}
        onPickFingerprint={onPickFingerprint}
      />

      <DlqFilters value={filters} onChange={setFilters} />

      <section
        data-testid="dlq-bulk-toolbar"
        className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-gray-200 bg-white p-3 shadow-sm"
      >
        <div className="text-sm text-gray-700">
          <span data-testid="dlq-selection-count" className="font-medium">
            {selected.size}
          </span>{" "}
          selected of {filtered.length} shown ({total} total in DLQ)
        </div>
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={onReplay}
            disabled={selected.size === 0 || acting}
            data-testid="dlq-bulk-replay"
            className="rounded bg-blue-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-700 disabled:cursor-not-allowed disabled:opacity-50"
          >
            Replay selected
          </button>
          <button
            type="button"
            onClick={onDeleteRequested}
            disabled={selected.size === 0 || acting}
            data-testid="dlq-bulk-delete"
            className="rounded border border-red-300 bg-white px-3 py-1.5 text-sm font-medium text-red-700 hover:bg-red-50 disabled:cursor-not-allowed disabled:opacity-50"
          >
            Discard selected
          </button>
        </div>
      </section>

      <section className="overflow-hidden rounded-lg border border-gray-200 bg-white shadow-sm">
        <table className="min-w-full">
          <thead className="bg-gray-50 text-left text-xs uppercase tracking-wide text-gray-600">
            <tr>
              <th className="px-2 py-2">
                <input
                  type="checkbox"
                  data-testid="dlq-select-all"
                  aria-label="Select all rows"
                  checked={allSelected}
                  onChange={(e) => toggleSelectAll(e.target.checked)}
                  className="h-4 w-4 cursor-pointer rounded border-gray-300"
                />
              </th>
              <th className="px-2 py-2">ID</th>
              <th className="px-2 py-2">Source</th>
              <th className="px-2 py-2">Type</th>
              <th className="px-2 py-2">Received</th>
              <th className="px-2 py-2">Age</th>
              <th className="px-2 py-2 text-right">Attempts</th>
              <th className="px-2 py-2">Last error</th>
            </tr>
          </thead>
          <tbody data-testid="dlq-tbody">
            {filtered.length === 0 ? (
              <tr>
                <td colSpan={8} className="px-4 py-6 text-center text-sm text-gray-500">
                  {rows.length === 0
                    ? "DLQ is empty -- nothing to triage."
                    : "Filters hide every loaded row. Reset to see them."}
                </td>
              </tr>
            ) : (
              filtered.map((row) => (
                <DlqRow
                  key={row.id}
                  row={row}
                  selected={selected.has(row.id)}
                  onSelectChange={toggleSelectOne}
                  outcome={outcomes.get(row.id)}
                  now={now}
                  fetchDetail={fetchDetailFn}
                />
              ))
            )}
          </tbody>
        </table>
      </section>

      <footer className="flex items-center justify-between text-xs text-gray-600">
        <span>
          Showing rows {total === 0 ? 0 : offset + 1}-
          {Math.min(offset + PAGE_SIZE, total)} of {total}
        </span>
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={onPrev}
            disabled={offset === 0 || loading}
            data-testid="dlq-pagination-prev"
            className="rounded border border-gray-300 bg-white px-2 py-1 hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-50"
          >
            Previous
          </button>
          <button
            type="button"
            onClick={onNext}
            disabled={offset + PAGE_SIZE >= total || loading}
            data-testid="dlq-pagination-next"
            className="rounded border border-gray-300 bg-white px-2 py-1 hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-50"
          >
            Next
          </button>
        </div>
      </footer>

      <ConfirmDeleteModal
        open={confirmDeleteOpen}
        count={selected.size}
        onConfirm={onDeleteConfirmed}
        onCancel={onDeleteCancelled}
      />
    </div>
  );
}
