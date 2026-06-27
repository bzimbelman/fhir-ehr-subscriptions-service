"use client";

import Link from "next/link";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { SubscriptionStatusPill } from "@/components/SubscriptionStatusPill";
import {
  fetchSubscriptionsHealth,
  type ApiResult,
} from "@/lib/subscriptionsClient";
import {
  formatDeliverySuccessRate,
  relativeTime,
  routeIdFor,
  truncate,
} from "@/lib/subscriptionFormat";
import type {
  SubscriptionHealthRow,
  SubscriptionsHealthEnvelope,
} from "@/lib/subscriptionTypes";

/**
 * Operator subscriptions list view (Epic #398, ticket #404).
 *
 * Layout follows the Mirth message-browser reference
 * (`docs/ui-design/reference-screens/04-message-browser.md`): a
 * filter bar across the top, then a table of rows. Detail
 * navigation lands on /subscriptions/[id].
 *
 * Polling: the same 30s + visibility-paused strategy as the dashboard,
 * implemented inline since the subscriptions screen is the only
 * other one in the app today and the abstraction isn't yet
 * justified.
 *
 * Filters are URL-driven via useState rather than the router so we
 * don't re-render the page on every keystroke. If we later want
 * deep-linkable filters, switch to `useSearchParams`.
 *
 * Caveat surface: the success-rate column reads 0/0 today (HAPI
 * `$status` isn't wired). The spec calls for rendering "—" in that
 * case — see `formatDeliverySuccessRate`.
 */

export const REFRESH_MS = 30_000;

interface SubscriptionsListProps {
  /** Test seam: a fake fetcher replacing the real network call. */
  fetcher?: () => Promise<ApiResult<SubscriptionsHealthEnvelope>>;
  /** Reference clock for the relative-time column (deterministic tests). */
  nowProvider?: () => Date;
  /** Disable the auto-refresh + visibility wiring for tests. */
  enableAutoRefresh?: boolean;
}

type StatusFilter = "all" | "active" | "off" | "requested" | "error";

export function SubscriptionsList({
  fetcher,
  nowProvider,
  enableAutoRefresh = true,
}: SubscriptionsListProps) {
  const effectiveFetcher = fetcher ?? fetchSubscriptionsHealth;

  const [envelope, setEnvelope] = useState<SubscriptionsHealthEnvelope | null>(
    null,
  );
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState<boolean>(true);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [channelFilter, setChannelFilter] = useState<string>("all");
  const abortRef = useRef<AbortController | null>(null);

  const refresh = useCallback(async () => {
    abortRef.current?.abort();
    const ctrl = new AbortController();
    abortRef.current = ctrl;
    setLoading(true);
    const result = await effectiveFetcher();
    if (ctrl.signal.aborted) return;
    setEnvelope(result.data);
    setError(result.error);
    setLoading(false);
  }, [effectiveFetcher]);

  useEffect(() => {
    let cancelled = false;
    const tick = async () => {
      if (cancelled) return;
      if (
        typeof document !== "undefined" &&
        document.visibilityState === "hidden"
      ) {
        return;
      }
      await refresh();
    };
    tick();
    if (!enableAutoRefresh) return () => { cancelled = true; abortRef.current?.abort(); };
    const interval = setInterval(tick, REFRESH_MS);
    const onVisibility = () => {
      if (document.visibilityState === "visible") tick();
    };
    if (typeof document !== "undefined") {
      document.addEventListener("visibilitychange", onVisibility);
    }
    return () => {
      cancelled = true;
      clearInterval(interval);
      if (typeof document !== "undefined") {
        document.removeEventListener("visibilitychange", onVisibility);
      }
      abortRef.current?.abort();
    };
  }, [refresh, enableAutoRefresh]);

  // Stable items reference: useMemo against `envelope?.items` rather
  // than the literal `?? []` so the downstream useMemos don't tear
  // every render on a falsy envelope.
  const items = useMemo(() => envelope?.items ?? [], [envelope]);

  const channels = useMemo(() => {
    const set = new Set<string>();
    for (const row of items) set.add(row.channel_type);
    return Array.from(set).sort();
  }, [items]);

  const visibleItems = useMemo(() => {
    return items.filter((row) => {
      if (statusFilter !== "all" && row.status !== statusFilter) return false;
      if (channelFilter !== "all" && row.channel_type !== channelFilter) {
        return false;
      }
      return true;
    });
  }, [items, statusFilter, channelFilter]);

  const refNow = nowProvider ? nowProvider() : new Date();

  return (
    <section className="space-y-4" data-testid="subscriptions-list">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold text-gray-900">Subscriptions</h1>
          <p className="text-sm text-gray-600">
            Registered FHIR Subscriptions and their delivery health.
          </p>
        </div>
        <button
          type="button"
          onClick={refresh}
          disabled={loading}
          data-testid="subscriptions-refresh"
          className="rounded border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-800 hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-60"
        >
          {loading ? "Refreshing…" : "Refresh"}
        </button>
      </header>

      <div className="flex flex-wrap items-center gap-3 rounded-lg border border-gray-200 bg-white p-3 shadow-sm">
        <label className="flex items-center gap-2 text-sm text-gray-700">
          <span className="font-medium">Status</span>
          <select
            data-testid="filter-status"
            value={statusFilter}
            onChange={(e) => setStatusFilter(e.target.value as StatusFilter)}
            className="rounded border border-gray-300 bg-white px-2 py-1 text-sm"
          >
            <option value="all">all</option>
            <option value="active">active</option>
            <option value="off">off</option>
            <option value="requested">requested</option>
            <option value="error">error</option>
          </select>
        </label>
        <label className="flex items-center gap-2 text-sm text-gray-700">
          <span className="font-medium">Channel</span>
          <select
            data-testid="filter-channel"
            value={channelFilter}
            onChange={(e) => setChannelFilter(e.target.value)}
            className="rounded border border-gray-300 bg-white px-2 py-1 text-sm"
          >
            <option value="all">all</option>
            {channels.map((c) => (
              <option key={c} value={c}>
                {c}
              </option>
            ))}
          </select>
        </label>
        <span className="ml-auto text-xs text-gray-500">
          {envelope ? `${visibleItems.length} of ${envelope.total}` : null}
        </span>
      </div>

      {error ? (
        <p
          role="alert"
          data-testid="subscriptions-error"
          className="rounded border border-red-200 bg-red-50 p-3 text-sm text-red-700"
        >
          Failed to load subscriptions: {error}
        </p>
      ) : null}

      {envelope && envelope.total === 0 ? <EmptyState /> : null}

      {envelope && envelope.total > 0 ? (
        <div className="overflow-x-auto rounded-lg border border-gray-200 bg-white shadow-sm">
          <table className="min-w-full divide-y divide-gray-200 text-sm">
            <thead className="bg-gray-50">
              <tr>
                <Th>Subscription</Th>
                <Th>Channel</Th>
                <Th>Endpoint</Th>
                <Th>Criteria</Th>
                <Th>Status</Th>
                <Th>Last delivery</Th>
                <Th>Success rate</Th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {visibleItems.map((row) => (
                <SubscriptionRow key={row.id} row={row} refNow={refNow} />
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
    </section>
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

function SubscriptionRow({
  row,
  refNow,
}: {
  row: SubscriptionHealthRow;
  refNow: Date;
}) {
  const bareId = routeIdFor(row.id);
  const lastOutcome = row.last_attempt_outcome;
  const outcomeBadge =
    lastOutcome === "success" ? (
      <span className="inline-flex items-center rounded-full bg-green-100 px-2 py-0.5 text-xs font-medium text-green-800 ring-1 ring-inset ring-green-300">
        success
      </span>
    ) : lastOutcome === "failure" ? (
      <span className="inline-flex items-center rounded-full bg-red-100 px-2 py-0.5 text-xs font-medium text-red-800 ring-1 ring-inset ring-red-300">
        failure
      </span>
    ) : (
      <span className="text-xs text-gray-500">—</span>
    );

  return (
    <tr data-testid={`subscription-row-${bareId}`} className="hover:bg-gray-50">
      <td className="px-3 py-2 font-mono text-xs">
        <Link
          href={`/subscriptions/${bareId}`}
          data-testid={`subscription-row-link-${bareId}`}
          className="text-blue-700 underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-blue-500"
        >
          {row.id}
        </Link>
      </td>
      <td className="px-3 py-2 text-gray-700">{row.channel_type}</td>
      <td
        className="px-3 py-2 font-mono text-xs text-gray-700"
        title={row.endpoint ?? undefined}
      >
        {truncate(row.endpoint, 50)}
      </td>
      <td
        className="px-3 py-2 font-mono text-xs text-gray-700"
        title={row.criteria}
      >
        {truncate(row.criteria, 50)}
      </td>
      <td className="px-3 py-2">
        <SubscriptionStatusPill status={row.status} />
      </td>
      <td className="px-3 py-2 text-gray-700">
        <div className="flex items-center gap-2">
          <span className="text-xs text-gray-500">
            {relativeTime(row.last_attempt_at, refNow)}
          </span>
          {outcomeBadge}
        </div>
      </td>
      <td
        className="px-3 py-2 tabular-nums text-gray-800"
        data-testid={`subscription-success-rate-${bareId}`}
      >
        {formatDeliverySuccessRate(row)}
      </td>
    </tr>
  );
}

/**
 * Empty state: shown when `total === 0`. The link to the external
 * subscribers guide is the documented onboarding path for a fresh
 * operator deployment with no subscriptions registered.
 */
function EmptyState() {
  return (
    <div
      data-testid="subscriptions-empty-state"
      className="rounded-lg border border-dashed border-gray-300 bg-gray-50 p-6 text-center"
    >
      <p className="mb-3 text-sm font-medium text-gray-800">
        No subscriptions are registered yet.
      </p>
      <p className="mx-auto max-w-prose text-sm text-gray-600">
        Subscriptions are registered by external subscribers POSTing
        FHIR <code className="rounded bg-white px-1 py-0.5">Subscription</code>{" "}
        resources. See the{" "}
        <a
          href="/docs/external-subscribers"
          data-testid="external-subscribers-link"
          className="text-blue-700 underline"
        >
          external subscribers guide
        </a>{" "}
        for the registration flow and example payloads.
      </p>
    </div>
  );
}
