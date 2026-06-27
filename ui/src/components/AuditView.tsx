"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  fetchAuditEvents,
  fetchAuditEvent,
  type ApiResult,
} from "@/lib/auditClient";
import type {
  AuditFilters,
  AuditSearchResponse,
  AuditEventRow,
} from "@/lib/auditTypes";

/**
 * Operator audit log view (Epic #398, ticket #407).
 *
 * Browses the FHIR AuditEvent resources HAPI persists (from #391, the
 * AuditEventInterceptor) through the normalised `/admin/audit` admin
 * endpoint. Layout:
 *
 *   - Filter strip at top (type / outcome / agent / date range)
 *   - Table: recorded / type+subtype / action / outcome / agent / entity
 *   - Click a row -> inline expansion fetches the full FHIR JSON via
 *     /admin/audit/{id}
 *   - Prev / Next pagination at the bottom (50 per page)
 *
 * No mutations. Audit data is append-only at the source; this view is a
 * read window onto the existing rows.
 */

const PAGE_SIZE = 50;

interface AuditViewProps {
  /** Test seam: a fake list fetcher replacing the real network call. */
  fetchList?: (
    filters: AuditFilters,
    limit: number,
    offset: number,
  ) => Promise<ApiResult<AuditSearchResponse>>;
  /** Test seam: a fake detail fetcher. */
  fetchDetail?: (id: string) => Promise<ApiResult<unknown>>;
}

export function AuditView({
  fetchList = fetchAuditEvents,
  fetchDetail = fetchAuditEvent,
}: AuditViewProps) {
  const [filters, setFilters] = useState<AuditFilters>({});
  const [offset, setOffset] = useState<number>(0);
  const [page, setPage] = useState<AuditSearchResponse | null>(null);
  const [pageError, setPageError] = useState<string | null>(null);
  const [loading, setLoading] = useState<boolean>(true);

  // expanded row id -> { loading, data, error }. One row at a time keeps
  // the page tidy; clicking another row collapses the first.
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const [expandedLoading, setExpandedLoading] = useState<boolean>(false);
  const [expandedData, setExpandedData] = useState<unknown | null>(null);
  const [expandedError, setExpandedError] = useState<string | null>(null);

  const reload = useCallback(
    async (f: AuditFilters, off: number) => {
      setLoading(true);
      const result = await fetchList(f, PAGE_SIZE, off);
      setPage(result.data);
      setPageError(result.error);
      setLoading(false);
    },
    [fetchList],
  );

  useEffect(() => {
    reload(filters, offset);
  }, [filters, offset, reload]);

  const onFilterChange = useCallback(
    (next: Partial<AuditFilters>) => {
      // Resetting offset to 0 on any filter change -- otherwise the new
      // filter set could leave us paged past the new (smaller) total.
      setFilters((prev) => ({ ...prev, ...next }));
      setOffset(0);
    },
    [],
  );

  const onRowClick = useCallback(
    async (row: AuditEventRow) => {
      if (expandedId === row.id) {
        setExpandedId(null);
        setExpandedData(null);
        setExpandedError(null);
        return;
      }
      setExpandedId(row.id);
      setExpandedLoading(true);
      setExpandedData(null);
      setExpandedError(null);
      const result = await fetchDetail(row.id);
      setExpandedData(result.data);
      setExpandedError(result.error);
      setExpandedLoading(false);
    },
    [expandedId, fetchDetail],
  );

  const total = page?.total ?? 0;
  const items = page?.items ?? [];
  const empty = !loading && total === 0 && !pageError;

  const pageStart = offset + 1;
  const pageEnd = Math.min(offset + items.length, total);
  const canPrev = offset > 0;
  const canNext = offset + PAGE_SIZE < total;

  return (
    <section className="space-y-4" data-testid="audit-view">
      <header>
        <h1 className="text-xl font-semibold text-gray-900">Audit log</h1>
        <p className="text-sm text-gray-600">
          FHIR AuditEvent records emitted by HAPI (ticket #391). Read-only.
        </p>
      </header>

      <FilterStrip filters={filters} onChange={onFilterChange} />

      {pageError ? (
        <p
          role="alert"
          data-testid="audit-fetch-error"
          className="rounded border border-red-200 bg-red-50 p-3 text-sm text-red-700"
        >
          Failed to load audit events: {pageError}
        </p>
      ) : null}

      {empty ? <EmptyState /> : null}

      {!empty && items.length > 0 ? (
        <div
          className="overflow-x-auto rounded-lg border border-gray-200 bg-white shadow-sm"
          data-testid="audit-table-wrap"
        >
          <table className="min-w-full divide-y divide-gray-200 text-sm">
            <thead className="bg-gray-50">
              <tr>
                <Th>Recorded</Th>
                <Th>Type / Subtype</Th>
                <Th>Action</Th>
                <Th>Outcome</Th>
                <Th>Agent</Th>
                <Th>Entity</Th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {items.map((row) => (
                <AuditRow
                  key={row.id}
                  row={row}
                  expanded={expandedId === row.id}
                  expandedLoading={expandedLoading}
                  expandedData={expandedData}
                  expandedError={expandedError}
                  onClick={() => onRowClick(row)}
                />
              ))}
            </tbody>
          </table>
        </div>
      ) : null}

      {!empty && total > 0 ? (
        <Pagination
          total={total}
          pageStart={pageStart}
          pageEnd={pageEnd}
          canPrev={canPrev}
          canNext={canNext}
          onPrev={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
          onNext={() => setOffset(offset + PAGE_SIZE)}
        />
      ) : null}
    </section>
  );
}

// -- subcomponents --------------------------------------------------------

function FilterStrip({
  filters,
  onChange,
}: {
  filters: AuditFilters;
  onChange: (next: Partial<AuditFilters>) => void;
}) {
  return (
    <div
      className="grid grid-cols-1 gap-3 rounded-lg border border-gray-200 bg-white p-3 shadow-sm md:grid-cols-3 lg:grid-cols-6"
      data-testid="audit-filter-strip"
    >
      <label className="block text-xs font-medium text-gray-700">
        Type
        <input
          type="text"
          value={filters.type ?? ""}
          onChange={(e) => onChange({ type: e.target.value || undefined })}
          placeholder="rest, 110100, ..."
          data-testid="audit-filter-type"
          className="mt-1 block w-full rounded border border-gray-300 bg-white px-2 py-1 text-sm"
        />
      </label>
      <label className="block text-xs font-medium text-gray-700">
        Outcome
        <select
          value={filters.outcome ?? ""}
          onChange={(e) => onChange({ outcome: e.target.value || undefined })}
          data-testid="audit-filter-outcome"
          className="mt-1 block w-full rounded border border-gray-300 bg-white px-2 py-1 text-sm"
        >
          <option value="">All</option>
          <option value="0">0 — Success</option>
          <option value="4">4 — Minor</option>
          <option value="8">8 — Serious</option>
          <option value="12">12 — Major</option>
        </select>
      </label>
      <label className="block text-xs font-medium text-gray-700">
        Agent
        <input
          type="text"
          value={filters.agent ?? ""}
          onChange={(e) => onChange({ agent: e.target.value || undefined })}
          placeholder="alice@example"
          data-testid="audit-filter-agent"
          className="mt-1 block w-full rounded border border-gray-300 bg-white px-2 py-1 text-sm"
        />
      </label>
      <label className="block text-xs font-medium text-gray-700">
        From
        <input
          type="date"
          value={filters.dateFrom ?? ""}
          onChange={(e) => onChange({ dateFrom: e.target.value || undefined })}
          data-testid="audit-filter-date-from"
          className="mt-1 block w-full rounded border border-gray-300 bg-white px-2 py-1 text-sm"
        />
      </label>
      <label className="block text-xs font-medium text-gray-700">
        To
        <input
          type="date"
          value={filters.dateTo ?? ""}
          onChange={(e) => onChange({ dateTo: e.target.value || undefined })}
          data-testid="audit-filter-date-to"
          className="mt-1 block w-full rounded border border-gray-300 bg-white px-2 py-1 text-sm"
        />
      </label>
    </div>
  );
}

function EmptyState() {
  return (
    <div
      className="rounded-lg border border-gray-200 bg-white p-6 text-sm text-gray-600 shadow-sm"
      data-testid="audit-empty"
    >
      <p className="font-medium text-gray-800">No AuditEvents yet.</p>
      <p className="mt-2">
        Audit rows are generated by the HAPI AuditEventInterceptor (ticket
        #391). A fresh install will show no events until the first FHIR
        write or, if reads/searches are captured, the first read. See{" "}
        <code className="rounded bg-gray-100 px-1 py-0.5 font-mono">
          docs/architecture.md#fhir-auditevent-generation
        </code>{" "}
        for the audit configuration knobs.
      </p>
    </div>
  );
}

function AuditRow({
  row,
  expanded,
  expandedLoading,
  expandedData,
  expandedError,
  onClick,
}: {
  row: AuditEventRow;
  expanded: boolean;
  expandedLoading: boolean;
  expandedData: unknown | null;
  expandedError: string | null;
  onClick: () => void;
}) {
  const bareId = row.id.includes("/") ? row.id.split("/").slice(-1)[0] : row.id;
  return (
    <>
      <tr
        data-testid={`audit-row-${bareId}`}
        className="cursor-pointer hover:bg-gray-50"
        onClick={onClick}
      >
        <td
          className="px-3 py-2 font-mono text-xs text-gray-700"
          title={row.recorded ?? undefined}
        >
          {row.recorded ?? "—"}
        </td>
        <td className="px-3 py-2 text-gray-800">
          <div>{row.type_display ?? row.type_code ?? "—"}</div>
          <div className="text-xs text-gray-500">
            {row.subtype_code ?? ""}
          </div>
        </td>
        <td className="px-3 py-2">
          <ActionBadge action={row.action} />
        </td>
        <td className="px-3 py-2">
          <OutcomePill outcome={row.outcome} display={row.outcome_display} />
        </td>
        <td className="px-3 py-2 text-gray-800">
          <div>{row.agent_name ?? "—"}</div>
          <div className="font-mono text-xs text-gray-500">
            {row.agent_who ?? ""}
          </div>
        </td>
        <td className="px-3 py-2 font-mono text-xs text-gray-700">
          {row.entity_what ?? "—"}
        </td>
      </tr>
      {expanded ? (
        <tr data-testid={`audit-row-${bareId}-expanded`}>
          <td colSpan={6} className="bg-gray-50 px-3 py-3">
            {expandedLoading ? (
              <p className="text-xs text-gray-500">Loading raw resource…</p>
            ) : expandedError ? (
              <p
                role="alert"
                className="rounded border border-red-200 bg-red-50 p-2 text-xs text-red-700"
              >
                {expandedError}
              </p>
            ) : expandedData ? (
              <pre
                data-testid={`audit-row-${bareId}-json`}
                className="max-h-96 overflow-auto rounded border border-gray-200 bg-white p-3 font-mono text-xs"
              >
                {JSON.stringify(expandedData, null, 2)}
              </pre>
            ) : (
              <p className="text-xs text-gray-500">No detail returned.</p>
            )}
          </td>
        </tr>
      ) : null}
    </>
  );
}

function ActionBadge({ action }: { action: string | null }) {
  if (!action) {
    return <span className="text-xs text-gray-400">—</span>;
  }
  return (
    <span
      data-testid={`audit-action-${action}`}
      className="inline-flex items-center rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-700 ring-1 ring-inset ring-gray-300"
    >
      {action}
    </span>
  );
}

/**
 * Color-coded outcome pill. The four FHIR R4 codes have a natural
 * green/yellow/orange/red ramp. Anything else falls back to gray.
 */
function OutcomePill({
  outcome,
  display,
}: {
  outcome: string | null;
  display: string | null;
}) {
  const label = display ?? outcome ?? "—";
  // Stable testid suffix so the test can reach a specific pill regardless
  // of the visible text.
  const suffix = outcome ?? "unknown";
  const className = useMemo(() => {
    switch (outcome) {
      case "0":
        return "inline-flex items-center rounded-full bg-green-100 px-2 py-0.5 text-xs font-medium text-green-800 ring-1 ring-inset ring-green-300";
      case "4":
        return "inline-flex items-center rounded-full bg-yellow-100 px-2 py-0.5 text-xs font-medium text-yellow-800 ring-1 ring-inset ring-yellow-300";
      case "8":
        return "inline-flex items-center rounded-full bg-orange-100 px-2 py-0.5 text-xs font-medium text-orange-800 ring-1 ring-inset ring-orange-300";
      case "12":
        return "inline-flex items-center rounded-full bg-red-100 px-2 py-0.5 text-xs font-medium text-red-800 ring-1 ring-inset ring-red-300";
      default:
        return "inline-flex items-center rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-700 ring-1 ring-inset ring-gray-300";
    }
  }, [outcome]);
  return (
    <span data-testid={`audit-outcome-${suffix}`} className={className}>
      {label}
    </span>
  );
}

function Pagination({
  total,
  pageStart,
  pageEnd,
  canPrev,
  canNext,
  onPrev,
  onNext,
}: {
  total: number;
  pageStart: number;
  pageEnd: number;
  canPrev: boolean;
  canNext: boolean;
  onPrev: () => void;
  onNext: () => void;
}) {
  return (
    <div
      className="flex items-center justify-between gap-3 rounded-lg border border-gray-200 bg-white p-3 text-sm shadow-sm"
      data-testid="audit-pagination"
    >
      <p className="text-gray-700">
        {pageStart}–{pageEnd} of {total}
      </p>
      <div className="flex gap-2">
        <button
          type="button"
          onClick={onPrev}
          disabled={!canPrev}
          data-testid="audit-page-prev"
          className="rounded border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-800 hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-60"
        >
          Previous
        </button>
        <button
          type="button"
          onClick={onNext}
          disabled={!canNext}
          data-testid="audit-page-next"
          className="rounded border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-800 hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-60"
        >
          Next
        </button>
      </div>
    </div>
  );
}

function Th({ children }: { children: React.ReactNode }) {
  return (
    <th
      scope="col"
      className="px-3 py-2 text-left text-xs font-semibold uppercase tracking-wide text-gray-600"
    >
      {children}
    </th>
  );
}
