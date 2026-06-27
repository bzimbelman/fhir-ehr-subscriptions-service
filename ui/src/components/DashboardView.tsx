"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { ComponentHealthList } from "@/components/ComponentHealthList";
import { RecentActivity } from "@/components/RecentActivity";
import { StatCard } from "@/components/StatCard";
import { StatusPill } from "@/components/StatusPill";
import {
  activeSubscriptionsCount,
  componentHealthRows,
  dlqSize,
  formatRate,
  rollupSystemStatus,
} from "@/lib/dashboardMetrics";
import {
  fetchDashboardSnapshot,
  type DashboardSnapshotWithTotals,
} from "@/lib/dashboardClient";
import type { SystemStatus } from "@/lib/dashboardTypes";

/**
 * Main dashboard client component.
 *
 * Polling strategy:
 *   - Initial fetch on mount.
 *   - Re-poll every REFRESH_MS while document.visibilityState === "visible".
 *   - When the tab is hidden (`document.hidden`), polling pauses. On
 *     re-visibility we kick off an immediate refresh.
 *   - Manual refresh button forces an immediate fetch regardless of state.
 *   - Concurrent fetches are cancelled via AbortController.
 *
 * The polling-when-visible pattern is documented in CommonJS sources at
 * https://developer.mozilla.org/.../visibilitychange and is the cheapest way
 * to drop background load to zero when the operator alt-tabs away.
 *
 * The snapshot/snapshot-error wiring is intentional: each upstream call has
 * its own error string in the snapshot, so a transient 500 on `/admin/messages`
 * doesn't blank the dashboard -- we just render the rest and surface a banner
 * in the recent-activity card.
 */

export const REFRESH_MS = 30_000;

interface DashboardViewProps {
  username: string | null;
  deploymentName?: string;
  /** Server action for sign-out (rendered inside the top-bar). */
  signOutSlot: React.ReactNode;
  /**
   * Test seam: a fake fetcher replacing the real network call. Production
   * code never sets this; tests inject a deterministic stub.
   */
  fetchSnapshot?: (opts: {
    signal?: AbortSignal;
  }) => Promise<DashboardSnapshotWithTotals>;
  /** Test seam: an externally-controlled "now" for deterministic rendering. */
  nowProvider?: () => Date;
  /** Test seam: skip the visibility-change wiring (tests assert it separately). */
  enableVisibilityRefresh?: boolean;
  /** Override the system-status pill (so the topbar/page can read & display it). */
  onStatusChange?: (status: SystemStatus) => void;
}

export function DashboardView({
  username,
  deploymentName,
  signOutSlot,
  fetchSnapshot = fetchDashboardSnapshot,
  nowProvider,
  enableVisibilityRefresh = true,
  onStatusChange,
}: DashboardViewProps) {
  const [snapshot, setSnapshot] = useState<DashboardSnapshotWithTotals | null>(
    null,
  );
  const [loading, setLoading] = useState<boolean>(true);
  const [error, setError] = useState<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);

  const refresh = useCallback(async () => {
    abortRef.current?.abort();
    const ctrl = new AbortController();
    abortRef.current = ctrl;
    setLoading(true);
    try {
      const snap = await fetchSnapshot({ signal: ctrl.signal });
      if (ctrl.signal.aborted) return;
      setSnapshot(snap);
      setError(null);
    } catch (e) {
      if ((e as Error).name === "AbortError") return;
      setError((e as Error).message);
    } finally {
      if (!ctrl.signal.aborted) setLoading(false);
    }
  }, [fetchSnapshot]);

  // Initial fetch + interval polling, paused while the tab is hidden.
  useEffect(() => {
    let cancelled = false;

    const tick = async () => {
      if (cancelled) return;
      // Skip the call when the tab is hidden -- saves load on the backend
      // and stops piling up stale AbortController instances.
      if (
        typeof document !== "undefined" &&
        document.visibilityState === "hidden"
      ) {
        return;
      }
      await refresh();
    };

    // Kick off the initial fetch immediately.
    tick();

    const interval = setInterval(tick, REFRESH_MS);

    const onVisibility = () => {
      if (document.visibilityState === "visible") {
        tick();
      }
    };
    if (enableVisibilityRefresh && typeof document !== "undefined") {
      document.addEventListener("visibilitychange", onVisibility);
    }

    return () => {
      cancelled = true;
      clearInterval(interval);
      if (enableVisibilityRefresh && typeof document !== "undefined") {
        document.removeEventListener("visibilitychange", onVisibility);
      }
      abortRef.current?.abort();
    };
  }, [refresh, enableVisibilityRefresh]);

  // Tell the parent (page header / top-bar) whenever our derived system
  // status changes -- the pill lives in the top-bar above us, but the data
  // it reads is owned here.
  const systemStatus: SystemStatus = snapshot
    ? rollupSystemStatus(snapshot.system, snapshot.actuator)
    : "UNKNOWN";
  useEffect(() => {
    if (onStatusChange) onStatusChange(systemStatus);
  }, [systemStatus, onStatusChange]);

  const dlq = snapshot ? dlqSize(snapshot.system) : 0;
  const activeSubs = snapshot
    ? activeSubscriptionsCount(snapshot.subscriptionsHealth)
    : 0;
  const rows = snapshot
    ? componentHealthRows(snapshot.system, snapshot.actuator)
    : [];

  return (
    <div className="space-y-6">
      <header
        data-testid="dashboard-top-bar"
        className="-mx-6 -mt-6 mb-2 flex flex-wrap items-center justify-between gap-3 border-b border-gray-200 bg-white px-6 py-3"
      >
        <div className="flex items-center gap-3">
          <span className="text-base font-semibold text-gray-900">
            subscription-service
          </span>
          <span className="text-sm text-gray-500">
            {deploymentName ?? "operator console"}
          </span>
        </div>
        <div className="flex items-center gap-3">
          <span className="text-xs font-medium uppercase tracking-wide text-gray-500">
            System
          </span>
          <StatusPill status={systemStatus} />
        </div>
        <div className="flex items-center gap-3">
          {username ? (
            <span
              className="text-sm text-gray-700"
              data-testid="topbar-username"
            >
              {username}
            </span>
          ) : null}
          {signOutSlot}
        </div>
      </header>

      <section
        aria-labelledby="stats-heading"
        data-testid="stats-row"
        className="space-y-3"
      >
        <div className="flex items-center justify-between">
          <h2
            id="stats-heading"
            className="text-sm font-semibold uppercase tracking-wide text-gray-700"
          >
            At a glance
          </h2>
          <button
            type="button"
            onClick={refresh}
            disabled={loading}
            data-testid="refresh-button"
            aria-label="Refresh dashboard"
            className="rounded border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-800 hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-60"
          >
            {loading ? "Refreshing…" : "Refresh"}
          </button>
        </div>

        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6">
          <StatCard
            label="Today"
            value={snapshot ? snapshot.totalsToday : null}
            hint="Messages received"
          />
          <StatCard
            label="This week"
            value={snapshot ? snapshot.totalsWeek : null}
            hint="7d rolling"
          />
          <StatCard
            label="This month"
            value={snapshot ? snapshot.totalsMonth : null}
            hint="30d rolling"
          />
          <StatCard
            label="Success today"
            value={snapshot ? formatRate(snapshot.successRateToday) : null}
            hint="delivered / total"
          />
          <StatCard
            label="DLQ size"
            value={snapshot ? dlq : null}
            hint="dead-letter rows"
            tone={dlq > 0 ? "danger" : "default"}
          />
          <StatCard
            label="Active subs"
            value={snapshot ? activeSubs : null}
            hint="subscriptions"
          />
        </div>

        {error ? (
          <p className="text-sm text-red-700" role="alert">
            Failed to refresh: {error}
          </p>
        ) : null}
      </section>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <ComponentHealthList rows={rows} error={snapshot?.systemError ?? null} />
        <RecentActivity
          items={snapshot?.recentMessages ?? []}
          error={snapshot?.recentError ?? null}
          now={nowProvider?.()}
        />
      </div>

      <footer className="text-xs text-gray-500">
        {snapshot
          ? `Last updated ${new Date(snapshot.fetchedAt).toLocaleTimeString()} · auto-refresh every ${REFRESH_MS / 1000}s (paused when tab hidden)`
          : "Loading…"}
      </footer>
    </div>
  );
}
