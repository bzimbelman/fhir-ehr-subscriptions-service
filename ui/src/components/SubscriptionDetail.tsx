"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { SubscriptionStatusPill } from "@/components/SubscriptionStatusPill";
import {
  fetchSubscriptionHistory,
  fetchSubscriptionResource,
  patchSubscriptionStatus,
  type ApiResult,
} from "@/lib/subscriptionsClient";
import { relativeTime } from "@/lib/subscriptionFormat";
import type {
  FhirSubscriptionResource,
  SubscriptionHistoryEnvelope,
} from "@/lib/subscriptionTypes";

/**
 * Per-subscription detail page (Epic #398, ticket #404).
 *
 * Sections:
 *   1. Header — id, status pill, channel, endpoint, criteria
 *   2. Configuration panel — full FHIR Subscription resource JSON
 *   3. Recent deliveries — last 50 attempts (mirrors Mirth message-detail
 *      reference, narrower scope — see
 *      docs/ui-design/reference-screens/05-message-detail.md)
 *   4. Toggle status — flip active <-> off via PATCH
 *   5. Manual trigger panel — v1 is a no-op: shows the curl an operator
 *      would run to fire a synthetic notification. Spec calls this out
 *      explicitly as a "stub for v1", with the actual fire-a-test action
 *      deferred. See the comment on TriggerPanel below.
 *
 * Data: three independent fetches (history, resource, plus the row that
 * was passed in). Each renders best-effort with its own error state so
 * a 5xx on one section doesn't blank the page.
 */

interface SubscriptionDetailProps {
  /** Bare HAPI id (no "Subscription/" prefix). */
  id: string;
  /** Test seam: replace `fetchSubscriptionHistory` for assertions. */
  fetchHistory?: typeof fetchSubscriptionHistory;
  /** Test seam: replace the resource fetcher. */
  fetchResource?: typeof fetchSubscriptionResource;
  /** Test seam: replace the status PATCH call. */
  patchStatus?: typeof patchSubscriptionStatus;
  /** Reference clock for relative-time strings. */
  nowProvider?: () => Date;
}

export function SubscriptionDetail({
  id,
  fetchHistory = fetchSubscriptionHistory,
  fetchResource = fetchSubscriptionResource,
  patchStatus = patchSubscriptionStatus,
  nowProvider,
}: SubscriptionDetailProps) {
  const [historyResult, setHistoryResult] = useState<
    ApiResult<SubscriptionHistoryEnvelope> | null
  >(null);
  const [resourceResult, setResourceResult] = useState<
    ApiResult<FhirSubscriptionResource> | null
  >(null);
  const [pending, setPending] = useState<boolean>(false);
  const [toggleError, setToggleError] = useState<string | null>(null);
  const [showTrigger, setShowTrigger] = useState<boolean>(false);

  const reloadResource = useCallback(async () => {
    const r = await fetchResource(id);
    setResourceResult(r);
  }, [fetchResource, id]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      const [hist, res] = await Promise.all([
        fetchHistory(id),
        fetchResource(id),
      ]);
      if (cancelled) return;
      setHistoryResult(hist);
      setResourceResult(res);
    })();
    return () => {
      cancelled = true;
    };
  }, [id, fetchHistory, fetchResource]);

  const onToggle = useCallback(async () => {
    if (!resourceResult?.data) return;
    const currentStatus = resourceResult.data.status ?? "off";
    const newStatus = currentStatus === "active" ? "off" : "active";
    setPending(true);
    setToggleError(null);
    const result = await patchStatus(id, newStatus);
    setPending(false);
    if (result.error) {
      setToggleError(result.error);
      return;
    }
    // Reload the resource so the header pill + button label reflect
    // the new state. We deliberately don't optimistically update —
    // the cost is one extra GET and we avoid drifting state if HAPI
    // rejected the update silently.
    await reloadResource();
  }, [id, patchStatus, reloadResource, resourceResult]);

  const refNow = nowProvider ? nowProvider() : new Date();
  const resource = resourceResult?.data;
  const status = resource?.status ?? "unknown";
  const channelType = resource?.channel?.type ?? "—";
  const endpoint = resource?.channel?.endpoint ?? "—";
  const criteria = resource?.criteria ?? "—";

  return (
    <section
      className="space-y-6"
      data-testid={`subscription-detail-${id}`}
    >
      <Link
        href="/subscriptions"
        className="inline-flex items-center text-sm text-blue-700 hover:underline"
      >
        ← Back to subscriptions
      </Link>

      <header className="space-y-2">
        <div className="flex flex-wrap items-center gap-3">
          <h1 className="font-mono text-lg font-semibold">
            Subscription/{id}
          </h1>
          <SubscriptionStatusPill status={status} />
        </div>
        <dl className="grid grid-cols-1 gap-3 text-sm sm:grid-cols-3">
          <Field label="Channel">{channelType}</Field>
          <Field label="Endpoint" mono>
            {endpoint}
          </Field>
          <Field label="Criteria" mono>
            {criteria}
          </Field>
        </dl>
        {resourceResult?.error ? (
          <p className="text-sm text-red-700" role="alert">
            Failed to load resource: {resourceResult.error}
          </p>
        ) : null}
      </header>

      <div className="flex flex-wrap items-center gap-3">
        <button
          type="button"
          data-testid="toggle-status-button"
          onClick={onToggle}
          disabled={pending || !resource}
          className="rounded bg-blue-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-700 disabled:cursor-not-allowed disabled:opacity-60"
        >
          {pending
            ? "Updating…"
            : status === "active"
              ? "Turn off"
              : "Activate"}
        </button>
        <button
          type="button"
          data-testid="manual-trigger-button"
          onClick={() => setShowTrigger((v) => !v)}
          className="rounded border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-800 hover:bg-gray-100"
        >
          {showTrigger ? "Hide test notification panel" : "Send a test notification"}
        </button>
        {toggleError ? (
          <p className="text-sm text-red-700" role="alert">
            Status update failed: {toggleError}
          </p>
        ) : null}
      </div>

      {showTrigger ? <TriggerPanel id={id} criteria={criteria} /> : null}

      <ResourceConfigPanel resource={resource} error={resourceResult?.error ?? null} />

      <HistoryTable
        result={historyResult}
        refNow={refNow}
      />
    </section>
  );
}

function Field({
  label,
  children,
  mono,
}: {
  label: string;
  children: React.ReactNode;
  mono?: boolean;
}) {
  return (
    <div>
      <dt className="text-xs font-medium uppercase tracking-wide text-gray-500">
        {label}
      </dt>
      <dd
        className={
          (mono ? "font-mono text-xs " : "text-sm ") + "text-gray-800 break-all"
        }
      >
        {children}
      </dd>
    </div>
  );
}

function ResourceConfigPanel({
  resource,
  error,
}: {
  resource: FhirSubscriptionResource | null | undefined;
  error: string | null;
}) {
  return (
    <section
      data-testid="subscription-config-panel"
      className="rounded-lg border border-gray-200 bg-white p-4 shadow-sm"
    >
      <h2 className="mb-3 text-sm font-semibold uppercase tracking-wide text-gray-700">
        Configuration (FHIR resource)
      </h2>
      {error ? (
        <p className="text-sm text-red-700">Failed: {error}</p>
      ) : !resource ? (
        <p className="text-sm text-gray-500">Loading…</p>
      ) : (
        <pre className="max-h-96 overflow-auto rounded bg-gray-50 p-3 text-xs text-gray-800">
          {JSON.stringify(resource, null, 2)}
        </pre>
      )}
    </section>
  );
}

function HistoryTable({
  result,
  refNow,
}: {
  result: ApiResult<SubscriptionHistoryEnvelope> | null;
  refNow: Date;
}) {
  const items = result?.data?.items ?? [];
  return (
    <section
      data-testid="subscription-history-table"
      className="rounded-lg border border-gray-200 bg-white p-4 shadow-sm"
    >
      <h2 className="mb-3 text-sm font-semibold uppercase tracking-wide text-gray-700">
        Recent deliveries
      </h2>
      {result?.error ? (
        <p className="text-sm text-red-700">Failed: {result.error}</p>
      ) : !result ? (
        <p className="text-sm text-gray-500">Loading…</p>
      ) : items.length === 0 ? (
        <p
          data-testid="history-empty-state"
          className="text-sm text-gray-600"
        >
          No delivery history available — HAPI 7.6&apos;s{" "}
          <code className="rounded bg-gray-100 px-1">$status</code> operation
          is not wired in our build, so per-attempt detail is not yet
          available. Follow-up: ticket #390.
        </p>
      ) : (
        <div className="overflow-x-auto">
          <table className="min-w-full divide-y divide-gray-200 text-sm">
            <thead className="bg-gray-50">
              <tr>
                <Th2>Attempted</Th2>
                <Th2>Outcome</Th2>
                <Th2>HTTP</Th2>
                <Th2>Duration</Th2>
                <Th2>Error</Th2>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {items.map((row, i) => (
                <tr key={i} className="hover:bg-gray-50">
                  <td className="px-3 py-2 text-xs text-gray-700">
                    {relativeTime(row.attempted_at, refNow)}
                  </td>
                  <td className="px-3 py-2 text-xs">
                    {row.outcome === "success" ? (
                      <span className="inline-flex items-center rounded-full bg-green-100 px-2 py-0.5 font-medium text-green-800 ring-1 ring-inset ring-green-300">
                        success
                      </span>
                    ) : (
                      <span className="inline-flex items-center rounded-full bg-red-100 px-2 py-0.5 font-medium text-red-800 ring-1 ring-inset ring-red-300">
                        {row.outcome}
                      </span>
                    )}
                  </td>
                  <td className="px-3 py-2 text-xs text-gray-700 tabular-nums">
                    {row.http_status ?? "—"}
                  </td>
                  <td className="px-3 py-2 text-xs text-gray-700 tabular-nums">
                    {row.duration_ms != null ? `${row.duration_ms}ms` : "—"}
                  </td>
                  <td className="max-w-md px-3 py-2 text-xs text-gray-700">
                    {row.error ?? "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function Th2({ children }: { children: React.ReactNode }) {
  return (
    <th
      scope="col"
      className="px-3 py-2 text-left text-xs font-semibold uppercase tracking-wide text-gray-600"
    >
      {children}
    </th>
  );
}

/**
 * Manual trigger panel — v1 stub.
 *
 * The ticket spec deliberately punts on actually firing a synthetic
 * notification. Reason: doing so safely requires synthesizing a
 * resource that matches the Subscription's criteria, which depends on
 * the criteria type (Patient? vs SubscriptionTopic vs custom search
 * params) — too broad for v1. Instead we show operators the curl
 * commands they can run themselves to either (a) re-fire the last
 * known event via HAPI's `$trigger` (deferred — same wiring gap as
 * `$status`), or (b) POST a matching resource so HAPI's normal
 * notification path runs.
 *
 * When ticket #390 (or its follow-up) wires `$status` + `$trigger`,
 * this panel grows a real button that POSTs to the new endpoint and
 * displays the synthesized notification body.
 */
function TriggerPanel({
  id,
  criteria,
}: {
  id: string;
  criteria: string;
}) {
  const curl = `# Option A — synthesize a matching resource and POST to HAPI.
# Replace 'criteria' below with one that matches: ${criteria || "(criteria not set)"}
curl -X POST -H "Content-Type: application/fhir+json" \\
  http://localhost:8080/fhir/Patient -d '{"resourceType":"Patient","name":[{"family":"Test"}]}'

# Option B — when HAPI exposes \\$trigger on Subscription (not wired in
# our build today — see ticket #390 / #404), call it directly:
curl -X POST \\
  http://localhost:8080/fhir/Subscription/${id}/\\$trigger`;
  return (
    <section
      data-testid="manual-trigger-panel"
      className="rounded-lg border border-yellow-300 bg-yellow-50 p-4"
    >
      <h2 className="mb-2 text-sm font-semibold text-yellow-900">
        Send a test notification (manual)
      </h2>
      <p className="mb-3 text-sm text-yellow-900">
        Automatic test-firing is deferred to a follow-up — HAPI 7.6&apos;s{" "}
        <code className="rounded bg-white px-1">$trigger</code> operation
        isn&apos;t wired in our build. In the meantime, run one of these
        commands against HAPI to exercise the delivery path:
      </p>
      <pre className="overflow-auto rounded bg-white p-3 text-xs text-gray-800">
        {curl}
      </pre>
    </section>
  );
}
