"use client";

import { useState } from "react";
import type { MessageSummary } from "@/lib/dashboardTypes";
import type { BulkActionOutcome, MessageDetail } from "@/lib/dlqTypes";
import { AgeBadge } from "@/components/AgeBadge";
import { ageBand, truncateError } from "@/lib/dlqUtils";
import { relativeTime } from "@/lib/dashboardMetrics";

/**
 * One row in the DLQ table. Two states:
 *   - collapsed (default): the summary line + checkbox.
 *   - expanded: inline panel below with raw_message + full last_error +
 *     attempt history placeholder. The detail lazily fetches from
 *     /api/admin/messages/{id} on first expansion.
 *
 * Click on the row body (not the checkbox) toggles the expansion. The
 * checkbox controls selection for bulk actions.
 *
 * NOTE: clicking the link icon goes to /messages/{id} which is a
 * placeholder route landing in ticket #402. For v1, the inline expansion
 * is the operator's primary "see the payload" surface.
 */
interface DlqRowProps {
  row: MessageSummary;
  selected: boolean;
  onSelectChange: (id: number, selected: boolean) => void;
  /** Latest bulk-action outcome for this row, if any. */
  outcome?: BulkActionOutcome;
  now: Date;
  fetchDetail: (id: number) => Promise<MessageDetail>;
}

export function DlqRow({
  row,
  selected,
  onSelectChange,
  outcome,
  now,
  fetchDetail,
}: DlqRowProps) {
  const [expanded, setExpanded] = useState(false);
  const [detail, setDetail] = useState<MessageDetail | null>(null);
  const [detailErr, setDetailErr] = useState<string | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);

  const band = ageBand(row.received_at, now);

  const toggle = async () => {
    const next = !expanded;
    setExpanded(next);
    if (next && detail === null && !detailLoading) {
      setDetailLoading(true);
      try {
        const d = await fetchDetail(row.id);
        setDetail(d);
        setDetailErr(null);
      } catch (e) {
        setDetailErr((e as Error).message);
      } finally {
        setDetailLoading(false);
      }
    }
  };

  return (
    <>
      <tr
        data-testid={`dlq-row-${row.id}`}
        className="border-t border-gray-100 hover:bg-gray-50"
      >
        <td className="px-2 py-2 align-top">
          <input
            type="checkbox"
            data-testid={`dlq-row-select-${row.id}`}
            aria-label={`Select message ${row.id}`}
            checked={selected}
            onChange={(e) => onSelectChange(row.id, e.target.checked)}
            className="h-4 w-4 cursor-pointer rounded border-gray-300"
          />
        </td>
        <td
          className="cursor-pointer px-2 py-2 align-top font-mono text-xs text-gray-900"
          onClick={toggle}
          data-testid={`dlq-row-id-${row.id}`}
        >
          {row.id}
        </td>
        <td
          className="cursor-pointer px-2 py-2 align-top text-sm text-gray-900"
          onClick={toggle}
        >
          <div className="flex flex-col">
            <span className="font-medium">{row.source_system}</span>
            <span className="text-xs text-gray-500">{row.source_protocol}</span>
          </div>
        </td>
        <td
          className="cursor-pointer px-2 py-2 align-top text-sm text-gray-800"
          onClick={toggle}
        >
          {row.message_type}
        </td>
        <td
          className="cursor-pointer px-2 py-2 align-top text-xs text-gray-700"
          onClick={toggle}
        >
          {relativeTime(row.received_at, now)}
        </td>
        <td
          className="cursor-pointer px-2 py-2 align-top"
          onClick={toggle}
          data-testid={`dlq-row-age-${row.id}`}
        >
          <AgeBadge band={band} label={bandLabel(band)} />
        </td>
        <td
          className="cursor-pointer px-2 py-2 align-top text-right text-xs tabular-nums text-gray-800"
          onClick={toggle}
        >
          {row.attempt_count}
        </td>
        <td
          className="cursor-pointer px-2 py-2 align-top text-xs text-gray-700"
          onClick={toggle}
          title={row.last_error ?? ""}
          data-testid={`dlq-row-last-error-${row.id}`}
        >
          <span className="block max-w-[28rem] truncate font-mono">
            {truncateError(row.last_error)}
          </span>
          {outcome ? (
            <span
              data-testid={`dlq-row-outcome-${row.id}`}
              className={`mt-1 inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-medium ring-1 ring-inset ${
                outcome.ok
                  ? "bg-green-100 text-green-800 ring-green-300"
                  : "bg-red-100 text-red-800 ring-red-300"
              }`}
            >
              {outcome.ok
                ? "replayed"
                : `failed${outcome.status ? ` (${outcome.status})` : ""}`}
            </span>
          ) : null}
        </td>
      </tr>
      {expanded ? (
        <tr
          data-testid={`dlq-row-expansion-${row.id}`}
          className="border-t border-gray-100 bg-gray-50"
        >
          <td colSpan={8} className="px-4 py-3">
            <DlqRowExpansion
              row={row}
              detail={detail}
              loading={detailLoading}
              error={detailErr}
            />
          </td>
        </tr>
      ) : null}
    </>
  );
}

function bandLabel(b: "green" | "yellow" | "red"): string {
  switch (b) {
    case "green":
      return "<1h";
    case "yellow":
      return "1-24h";
    case "red":
      return ">24h";
  }
}

function DlqRowExpansion({
  row,
  detail,
  loading,
  error,
}: {
  row: MessageSummary;
  detail: MessageDetail | null;
  loading: boolean;
  error: string | null;
}) {
  return (
    <div className="space-y-3">
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <Field label="Source ID">
          <code className="font-mono text-xs">{row.source_id}</code>
        </Field>
        <Field label="Correlation ID">
          <code className="font-mono text-xs">
            {row.correlation_id ?? "—"}
          </code>
        </Field>
        <Field label="Attempt count">
          <span className="tabular-nums">{row.attempt_count}</span>
        </Field>
        <Field label="Last attempt at">
          <span className="text-xs text-gray-700">
            {detail?.last_attempt_at ?? "—"}
          </span>
        </Field>
      </div>

      <div>
        <div className="mb-1 text-xs font-semibold uppercase tracking-wide text-gray-700">
          Last error (full)
        </div>
        <pre className="max-h-40 overflow-auto whitespace-pre-wrap break-words rounded bg-white p-2 font-mono text-xs text-red-900 ring-1 ring-inset ring-red-200">
          {row.last_error ?? "(none)"}
        </pre>
      </div>

      <div>
        <div className="mb-1 flex items-center justify-between">
          <div className="text-xs font-semibold uppercase tracking-wide text-gray-700">
            Raw message
          </div>
          {detail?.raw_content_type ? (
            <span className="text-xs text-gray-500">
              {detail.raw_content_type}
            </span>
          ) : null}
        </div>
        {loading ? (
          <p className="text-xs text-gray-500">Loading raw payload&hellip;</p>
        ) : error ? (
          <p className="text-xs text-red-700" role="alert">
            Failed to load message detail: {error}
          </p>
        ) : detail ? (
          <pre
            data-testid={`dlq-raw-${row.id}`}
            className="max-h-72 overflow-auto whitespace-pre-wrap break-words rounded bg-white p-2 font-mono text-xs text-gray-900 ring-1 ring-inset ring-gray-200"
          >
            {detail.raw_message}
          </pre>
        ) : (
          <p className="text-xs text-gray-500">Click the row to load.</p>
        )}
      </div>

      <div className="rounded border border-dashed border-gray-300 p-2 text-xs text-gray-600">
        <strong>Attempt history</strong> — the backend currently surfaces only
        the most recent attempt (count + timestamp + error). Per-attempt
        history is a separate story; this section will populate once that
        lands.
      </div>
    </div>
  );
}

function Field({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex flex-col">
      <span className="text-[10px] font-semibold uppercase tracking-wide text-gray-500">
        {label}
      </span>
      <span className="text-sm text-gray-900">{children}</span>
    </div>
  );
}
