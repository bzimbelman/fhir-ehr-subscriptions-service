"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { MessageStatusPill } from "@/components/MessageStatusPill";
import { relativeTime } from "@/lib/dashboardMetrics";
import {
  fetchMessageDetail as defaultFetchDetail,
  fetchMessageEffects as defaultFetchEffects,
  retryMessage as defaultRetry,
  deleteMessage as defaultDelete,
} from "@/lib/messagesClient";
import { durationMs } from "@/lib/messagesUtils";
import type {
  MessageDetailRow,
  MessageEffectsResponse,
} from "@/lib/messagesTypes";

/**
 * Single-message detail view (Epic #398, ticket #402).
 *
 * The page is the operator's deep-dive surface — it joins three data
 * sources:
 *
 *   1. The row from /admin/messages/{id} (raw_message + timestamps).
 *   2. The downstream effects from /admin/messages/{id}/effects (FHIR
 *      resources HAPI created + subscription notifications fired).
 *   3. A best-effort timeline assembled from the timestamps on (1) —
 *      the backend doesn't store per-transition events, so we render
 *      whichever instants the row carries.
 *
 * Layout:
 *
 *   - Header band: status pill, source, key timestamps, action buttons.
 *   - Two columns: raw inbound message (left), downstream effects (right).
 *   - Below: timeline of the row's lifecycle.
 *
 * Raw rendering follows the source protocol:
 *   - HL7V2_MLLP: split on \r and \n so each segment occupies one line.
 *   - FHIR_REST:  JSON.parse + stringify(_, _, 2) for pretty-print.
 *   - other:      render as-is.
 *
 * Test seams (`fetchDetailFn`, `fetchEffectsFn`, `retryFn`, `deleteFn`,
 * `confirmFn`, `reloadFn`, `nowProvider`) keep the view unit-testable.
 */

interface MessageDetailViewProps {
  id: string;
  fetchDetailFn?: typeof defaultFetchDetail;
  fetchEffectsFn?: typeof defaultFetchEffects;
  retryFn?: typeof defaultRetry;
  deleteFn?: typeof defaultDelete;
  /** Replaceable so tests can assert confirm prompts without `window.confirm`. */
  confirmFn?: (msg: string) => boolean;
  /** Replaceable reload hook so tests can assert "reloaded after action". */
  reloadFn?: () => Promise<void> | void;
  nowProvider?: () => Date;
}

export function MessageDetailView({
  id,
  fetchDetailFn = defaultFetchDetail,
  fetchEffectsFn = defaultFetchEffects,
  retryFn = defaultRetry,
  deleteFn = defaultDelete,
  confirmFn,
  reloadFn,
  nowProvider,
}: MessageDetailViewProps) {
  const [detail, setDetail] = useState<MessageDetailRow | null>(null);
  const [detailError, setDetailError] = useState<string | null>(null);
  const [effects, setEffects] = useState<MessageEffectsResponse | null>(null);
  const [effectsError, setEffectsError] = useState<string | null>(null);
  const [loading, setLoading] = useState<boolean>(true);
  const [acting, setActing] = useState<boolean>(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const [copied, setCopied] = useState<boolean>(false);

  const load = useCallback(async () => {
    setLoading(true);
    setDetailError(null);
    setEffectsError(null);
    const [detailRes, effectsRes] = await Promise.allSettled([
      fetchDetailFn(id),
      fetchEffectsFn(id),
    ]);
    if (detailRes.status === "fulfilled") {
      setDetail(detailRes.value);
    } else {
      setDetail(null);
      setDetailError((detailRes.reason as Error).message);
    }
    if (effectsRes.status === "fulfilled") {
      setEffects(effectsRes.value);
    } else {
      setEffects(null);
      setEffectsError((effectsRes.reason as Error).message);
    }
    setLoading(false);
  }, [fetchDetailFn, fetchEffectsFn, id]);

  useEffect(() => {
    void load();
  }, [load]);

  const runReload = useCallback(async () => {
    if (reloadFn) {
      await reloadFn();
    } else {
      await load();
    }
  }, [reloadFn, load]);

  const onRetry = useCallback(async () => {
    if (acting) return;
    setActing(true);
    setActionError(null);
    try {
      await retryFn(id);
      await runReload();
    } catch (e) {
      setActionError((e as Error).message);
    } finally {
      setActing(false);
    }
  }, [acting, retryFn, id, runReload]);

  const onDelete = useCallback(async () => {
    if (acting) return;
    const confirmer = confirmFn ?? ((m: string) => window.confirm(m));
    const ok = confirmer(
      `Permanently delete message ${id}? This cannot be undone.`,
    );
    if (!ok) return;
    setActing(true);
    setActionError(null);
    try {
      await deleteFn(id);
      await runReload();
    } catch (e) {
      setActionError((e as Error).message);
    } finally {
      setActing(false);
    }
  }, [acting, confirmFn, deleteFn, id, runReload]);

  const onCopy = useCallback(async () => {
    if (!detail?.raw_message) return;
    try {
      await navigator.clipboard.writeText(detail.raw_message);
      setCopied(true);
      // Reset the visual cue after a couple of seconds.
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // Some browsers reject clipboard writes outside user-gesture context;
      // a small inline error is enough — the operator can still select+copy.
      setActionError("Clipboard write failed (browser blocked).");
    }
  }, [detail?.raw_message]);

  const now = nowProvider ? nowProvider() : new Date();

  if (loading && !detail) {
    return (
      <p className="text-sm text-gray-500" data-testid="message-detail-loading">
        Loading message {id}…
      </p>
    );
  }

  if (detailError && !detail) {
    return (
      <section
        className="space-y-3"
        data-testid={`message-detail-error-${id}`}
      >
        <Link
          href="/messages"
          className="inline-flex items-center text-sm text-blue-700 hover:underline"
        >
          ← Back to messages
        </Link>
        <p role="alert" className="text-sm text-red-700">
          Failed to load message {id}: {detailError}
        </p>
      </section>
    );
  }

  if (!detail) {
    return null;
  }

  const dur = durationMs(detail.received_at, detail.delivered_at);
  const showRetry =
    detail.status === "FAILED" || detail.status === "DEAD_LETTER";
  const showDelete = detail.status === "DEAD_LETTER";

  return (
    <section
      className="space-y-6"
      data-testid={`message-detail-${id}`}
    >
      <Link
        href="/messages"
        className="inline-flex items-center text-sm text-blue-700 hover:underline"
      >
        ← Back to messages
      </Link>

      <header
        data-testid="message-detail-header"
        className="space-y-3 rounded-lg border border-gray-200 bg-white p-4 shadow-sm"
      >
        <div className="flex flex-wrap items-center gap-3">
          <MessageStatusPill status={detail.status} size="md" />
          <h1 className="font-mono text-lg font-semibold text-gray-900">
            Message {detail.id}
          </h1>
          <div className="ml-auto flex flex-wrap items-center gap-2">
            {showRetry ? (
              <button
                type="button"
                data-testid="message-action-retry"
                onClick={onRetry}
                disabled={acting}
                className="rounded bg-blue-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-700 disabled:cursor-not-allowed disabled:opacity-60"
              >
                {acting ? "Working…" : "Retry"}
              </button>
            ) : null}
            {showDelete ? (
              <button
                type="button"
                data-testid="message-action-delete"
                onClick={onDelete}
                disabled={acting}
                className="rounded border border-red-300 bg-white px-3 py-1.5 text-sm font-medium text-red-700 hover:bg-red-50 disabled:cursor-not-allowed disabled:opacity-60"
              >
                Delete
              </button>
            ) : null}
          </div>
        </div>

        <dl className="grid grid-cols-1 gap-3 text-sm sm:grid-cols-3">
          <Field label="Source">
            <span className="font-mono text-xs">
              {detail.source_system} / {detail.source_protocol} /{" "}
              {detail.source_id}
            </span>
          </Field>
          <Field label="Message type">{detail.message_type}</Field>
          <Field label="Correlation id" mono>
            {detail.correlation_id ?? "—"}
          </Field>
          <Field label="Received at">
            <span title={detail.received_at ?? ""}>
              {detail.received_at
                ? relativeTime(detail.received_at, now)
                : "—"}
            </span>
          </Field>
          <Field label="Delivered at">
            <span title={detail.delivered_at ?? ""}>
              {detail.delivered_at
                ? relativeTime(detail.delivered_at, now)
                : "—"}
            </span>
          </Field>
          <Field label="Duration">
            {dur != null ? `${dur} ms` : "—"}
          </Field>
          <Field label="Attempts">
            <span
              className={
                detail.attempt_count > 1
                  ? "font-semibold text-amber-700"
                  : "text-gray-900"
              }
              data-testid="message-detail-attempts"
            >
              {detail.attempt_count}
            </span>
          </Field>
          <Field label="Last attempt at">
            <span title={detail.last_attempt_at ?? ""}>
              {detail.last_attempt_at
                ? relativeTime(detail.last_attempt_at, now)
                : "—"}
            </span>
          </Field>
          <Field label="Next attempt at">
            <span title={detail.next_attempt_at ?? ""}>
              {detail.next_attempt_at
                ? relativeTime(detail.next_attempt_at, now)
                : "—"}
            </span>
          </Field>
        </dl>

        {detail.last_error ? (
          <div>
            <div className="mb-1 text-xs font-semibold uppercase tracking-wide text-gray-700">
              Last error
            </div>
            <pre
              data-testid="message-detail-last-error"
              className="max-h-40 overflow-auto whitespace-pre-wrap break-words rounded bg-red-50 p-2 font-mono text-xs text-red-900 ring-1 ring-inset ring-red-200"
            >
              {detail.last_error}
            </pre>
          </div>
        ) : null}

        {actionError ? (
          <p role="alert" className="text-sm text-red-700">
            Action failed: {actionError}
          </p>
        ) : null}
      </header>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <RawMessagePanel
          detail={detail}
          onCopy={onCopy}
          copied={copied}
        />
        <EffectsPanel effects={effects} error={effectsError} />
      </div>

      <TimelinePanel detail={detail} now={now} />
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
          (mono ? "font-mono text-xs " : "text-sm ") +
          "text-gray-800 break-all"
        }
      >
        {children}
      </dd>
    </div>
  );
}

/**
 * Render the raw inbound payload. The renderer decides protocol-specific
 * formatting:
 *   - HL7v2: each segment on its own line (split on `\r` AND `\n` because
 *     real-world wire payloads use either).
 *   - FHIR_REST: try JSON.parse + 2-space indent; on parse failure render
 *     verbatim.
 *   - otherwise: render verbatim.
 */
function RawMessagePanel({
  detail,
  onCopy,
  copied,
}: {
  detail: MessageDetailRow;
  onCopy: () => void;
  copied: boolean;
}) {
  const { rendered, label } = renderRaw(
    detail.source_protocol,
    detail.raw_message,
  );

  return (
    <section
      data-testid="message-raw-panel"
      className="rounded-lg border border-gray-200 bg-white p-4 shadow-sm"
    >
      <header className="mb-2 flex items-center justify-between">
        <h2 className="text-sm font-semibold uppercase tracking-wide text-gray-700">
          Raw inbound message
        </h2>
        <div className="flex items-center gap-2">
          <span
            className="rounded bg-gray-100 px-2 py-0.5 text-[10px] font-medium text-gray-700"
            data-testid="message-raw-label"
          >
            {label}
          </span>
          <button
            type="button"
            data-testid="message-raw-copy"
            onClick={onCopy}
            className="rounded border border-gray-300 bg-white px-2 py-0.5 text-xs text-gray-800 hover:bg-gray-100"
          >
            {copied ? "Copied" : "Copy"}
          </button>
        </div>
      </header>
      <pre
        data-testid="message-raw-body"
        className="max-h-[28rem] overflow-auto whitespace-pre rounded bg-gray-50 p-3 text-xs leading-relaxed text-gray-900 ring-1 ring-inset ring-gray-200"
      >
        {rendered}
      </pre>
    </section>
  );
}

/**
 * Pick the right rendering for `raw_message` based on `source_protocol`.
 *
 * Exported as a pure function so it could be unit-tested directly without
 * mounting the component; tests currently exercise it via the DOM.
 */
export function renderRaw(
  protocol: string,
  raw: string,
): { rendered: string; label: string } {
  if (protocol === "HL7V2_MLLP") {
    // HL7 v2 messages are normally CR-delimited on the wire; some inbound
    // routes normalize to LF. Split on either so a copy-pasted sample
    // still renders one segment per line.
    const segments = raw.split(/\r\n|\r|\n/).filter((seg) => seg.length > 0);
    return { rendered: segments.join("\n"), label: "HL7 v2.x" };
  }
  if (protocol === "FHIR_REST") {
    try {
      const parsed = JSON.parse(raw);
      return {
        rendered: JSON.stringify(parsed, null, 2),
        label: "FHIR JSON",
      };
    } catch {
      return { rendered: raw, label: "FHIR JSON (unparseable)" };
    }
  }
  return { rendered: raw, label: protocol || "raw" };
}

function EffectsPanel({
  effects,
  error,
}: {
  effects: MessageEffectsResponse | null;
  error: string | null;
}) {
  if (error) {
    return (
      <section
        data-testid="message-effects-panel"
        className="rounded-lg border border-gray-200 bg-white p-4 shadow-sm"
      >
        <h2 className="mb-2 text-sm font-semibold uppercase tracking-wide text-gray-700">
          Downstream effects
        </h2>
        <p className="text-sm text-red-700" role="alert">
          Failed to load effects: {error}
        </p>
      </section>
    );
  }

  const resources = effects?.fhir_resources_created ?? [];
  const subs = effects?.subscriptions_matched ?? [];
  const notifs = effects?.notifications_fired ?? [];
  const status = effects?.effects_status ?? "pending";

  const isEmpty =
    resources.length === 0 && subs.length === 0 && notifs.length === 0;

  return (
    <section
      data-testid="message-effects-panel"
      className="space-y-3 rounded-lg border border-gray-200 bg-white p-4 shadow-sm"
    >
      <header className="flex items-center justify-between">
        <h2 className="text-sm font-semibold uppercase tracking-wide text-gray-700">
          Downstream effects
        </h2>
        <span
          data-testid="message-effects-status"
          className="rounded bg-gray-100 px-2 py-0.5 text-[10px] font-medium text-gray-700"
        >
          {status}
        </span>
      </header>

      {isEmpty ? (
        <p
          data-testid="message-effects-empty"
          className="text-sm text-gray-600"
        >
          No downstream effects recorded yet.
        </p>
      ) : null}

      {resources.length > 0 ? (
        <div data-testid="message-effects-resources">
          <h3 className="mb-1 text-xs font-semibold uppercase tracking-wide text-gray-600">
            FHIR resources created / updated
          </h3>
          <ul className="space-y-1">
            {resources.map((r, i) => (
              <li
                key={`${r.id}-${i}`}
                className="rounded border border-gray-100 bg-gray-50 px-2 py-1 font-mono text-xs"
                data-testid={`message-effects-resource-${i}`}
              >
                <span className="font-semibold">{r.resource_type}</span>{" "}
                <span className="text-gray-700">{r.id}</span>
              </li>
            ))}
          </ul>
          <p className="mt-1 text-[10px] text-gray-500">
            HAPI links not wired in v1 — paste the path into the FHIR REST
            console to view the resource.
          </p>
        </div>
      ) : null}

      {notifs.length > 0 ? (
        <div data-testid="message-effects-notifications">
          <h3 className="mb-1 text-xs font-semibold uppercase tracking-wide text-gray-600">
            Subscription fires
          </h3>
          <ul className="space-y-1">
            {notifs.map((n, i) => (
              <li
                key={i}
                className="rounded border border-gray-100 bg-gray-50 px-2 py-1 text-xs"
                data-testid={`message-effects-notification-${i}`}
              >
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-mono">{n.subscription_id}</span>
                  <OutcomePill outcome={n.outcome} />
                  {n.attempted_at ? (
                    <span
                      className="text-[10px] text-gray-600"
                      title={n.attempted_at}
                    >
                      {n.attempted_at}
                    </span>
                  ) : null}
                </div>
                {n.endpoint ? (
                  <div className="mt-1 truncate font-mono text-[11px] text-gray-700">
                    {n.endpoint}
                  </div>
                ) : null}
                {n.error ? (
                  <div className="mt-1 font-mono text-[11px] text-red-700">
                    {n.error}
                  </div>
                ) : null}
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      {subs.length > 0 && notifs.length === 0 ? (
        // Show matched subscriptions even when no notifications fired —
        // helps the operator see "Subscription X matched but never sent".
        <div data-testid="message-effects-matched-subs">
          <h3 className="mb-1 text-xs font-semibold uppercase tracking-wide text-gray-600">
            Matched subscriptions (no fires recorded)
          </h3>
          <ul className="space-y-1">
            {subs.map((s, i) => (
              <li
                key={i}
                className="rounded border border-gray-100 bg-gray-50 px-2 py-1 font-mono text-xs"
              >
                {s.id} — {s.channel_type}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </section>
  );
}

function OutcomePill({ outcome }: { outcome: string }) {
  const ok = outcome === "success";
  return (
    <span
      className={
        "inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-medium ring-1 ring-inset " +
        (ok
          ? "bg-green-100 text-green-800 ring-green-300"
          : "bg-red-100 text-red-800 ring-red-300")
      }
    >
      {outcome}
    </span>
  );
}

/**
 * Timeline of the row's lifecycle. Best-effort: today the backend stores
 * only the row's own timestamps (received_at, last_attempt_at,
 * delivered_at) plus `attempt_count`. We render whichever instants the
 * row carries, in chronological order, and surface "Retried N times" as
 * an extra bullet when `attempt_count > 1`.
 *
 * Per-transition events (RECEIVED → TRANSFORMING → DELIVERED with
 * actual transition timestamps) is a deferred backend concern; this
 * panel will populate organically when that lands.
 */
function TimelinePanel({
  detail,
  now,
}: {
  detail: MessageDetailRow;
  now: Date;
}) {
  type Step = { label: string; at: string; cls: string };
  const steps: Step[] = [];
  if (detail.received_at) {
    steps.push({
      label: "Received",
      at: detail.received_at,
      cls: "bg-blue-500",
    });
  }
  if (detail.last_attempt_at) {
    steps.push({
      label: "Last attempt",
      at: detail.last_attempt_at,
      cls: "bg-indigo-500",
    });
  }
  if (detail.delivered_at) {
    steps.push({
      label: "Delivered",
      at: detail.delivered_at,
      cls: "bg-green-500",
    });
  }
  steps.sort((a, b) => new Date(a.at).getTime() - new Date(b.at).getTime());

  return (
    <section
      data-testid="message-timeline-panel"
      className="rounded-lg border border-gray-200 bg-white p-4 shadow-sm"
    >
      <h2 className="mb-3 text-sm font-semibold uppercase tracking-wide text-gray-700">
        Timeline
      </h2>
      {steps.length === 0 ? (
        <p className="text-sm text-gray-500">
          No timestamps recorded yet.
        </p>
      ) : (
        <ol className="space-y-2" data-testid="message-timeline-list">
          {steps.map((s, i) => (
            <li
              key={`${s.label}-${i}`}
              data-testid={`message-timeline-step-${s.label.toLowerCase().replace(/\s+/g, "-")}`}
              className="flex items-center gap-3 text-sm"
            >
              <span
                aria-hidden="true"
                className={`inline-block h-2.5 w-2.5 rounded-full ${s.cls}`}
              />
              <span className="font-medium text-gray-900">{s.label}</span>
              <span className="text-xs text-gray-500" title={s.at}>
                {relativeTime(s.at, now)}
              </span>
            </li>
          ))}
          {detail.attempt_count > 1 ? (
            <li
              data-testid="message-timeline-retries"
              className="ml-5 text-xs text-amber-700"
            >
              Retried {detail.attempt_count - 1}{" "}
              {detail.attempt_count - 1 === 1 ? "time" : "times"}
            </li>
          ) : null}
        </ol>
      )}
      <p className="mt-3 text-[11px] text-gray-500">
        The backend stores only the row&apos;s most-recent timestamps;
        per-transition events (RECEIVED → TRANSFORMING → DELIVERED with
        exact transition times) are a deferred follow-up. This panel
        renders whichever instants the row carries.
      </p>
    </section>
  );
}
