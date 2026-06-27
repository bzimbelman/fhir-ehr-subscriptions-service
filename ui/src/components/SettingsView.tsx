"use client";

import { useCallback, useEffect, useState } from "react";
import {
  fetchSystemSnapshot,
  fetchMatchboxHealthForSettings,
  type ApiResult,
} from "@/lib/settingsClient";
import type { SystemSnapshot } from "@/lib/settingsTypes";
import type { MatchboxHealth } from "@/lib/matchboxTypes";

/**
 * Operator Settings view (Epic #398, ticket #406).
 *
 * Read-only display of how this deployment is configured. Operators want
 * to answer at a glance: "is auth on? what validation mode? where's
 * HAPI? where's Matchbox?". There are no controls here -- configuration
 * changes go through env vars + redeploy, not the UI.
 *
 * Layout (top to bottom):
 *
 *   1. Feature toggles  - card grid (auth / validation / channel security
 *      / multitenancy + any extras the backend surfaces).
 *   2. Downstream URLs  - table (matchbox / hapi / auth issuer). Matchbox
 *      gets a "reachable" pill driven by a separate /admin/matchbox/health
 *      probe; HAPI and the auth issuer show URL only (see comments below
 *      for why).
 *   3. Schema versions  - observe API schema_version + log schema (the log
 *      schema is hardcoded for v1 since no endpoint surfaces it; see
 *      docs/observability/log-schema.md).
 *   4. Build info       - application version (from SystemSnapshot.version).
 *
 * Data flows through the same `/api/admin/[...path]` proxy as every other
 * page; the admin-API bearer token never reaches the browser.
 */

interface SettingsViewProps {
  /** Test seam: a fake system fetcher replacing the real network call. */
  fetchSystem?: () => Promise<ApiResult<SystemSnapshot>>;
  /** Test seam: a fake matchbox health fetcher. */
  fetchMatchbox?: () => Promise<ApiResult<MatchboxHealth>>;
}

// Static descriptions for the known feature toggles. Sourced from
// docs/architecture.md so the operator surface stays aligned with the
// design notes. Keyed by the toggle name from `/admin/observe/system`.
// Unknown toggles fall back to a neutral message (see UI below).
const TOGGLE_DESCRIPTIONS: Record<string, string> = {
  auth_enabled:
    "OIDC bearer-token gate on /fhir/*. When false, the FHIR API accepts unauthenticated requests (dev convenience only). See docs/auth.md.",
  validation_mode:
    "US Core profile validation: off (no validation), warn (validate + accept anyway), enforce (reject non-conforming bundles with 422). See docs/architecture.md#profile-validation-us-core.",
  channel_security_mode:
    "Subscription channel endpoint policy: strict (HTTPS + allowlist), relaxed (HTTPS-only), permissive (anything). See docs/architecture.md#subscription-channel-security.",
  multitenancy_mode:
    "HAPI partitioning: disabled (single global partition), enabled (one partition per tenant claim on the JWT). See docs/architecture.md#multi-tenancy.",
};

// Order the toggles are rendered in. Anything not in this list is
// appended at the end in insertion order.
const TOGGLE_ORDER = [
  "auth_enabled",
  "validation_mode",
  "channel_security_mode",
  "multitenancy_mode",
] as const;

export function SettingsView({
  fetchSystem = fetchSystemSnapshot,
  fetchMatchbox = fetchMatchboxHealthForSettings,
}: SettingsViewProps) {
  const [snapshot, setSnapshot] = useState<SystemSnapshot | null>(null);
  const [snapshotError, setSnapshotError] = useState<string | null>(null);
  const [snapshotLoading, setSnapshotLoading] = useState<boolean>(true);

  const [matchboxHealth, setMatchboxHealth] = useState<MatchboxHealth | null>(
    null,
  );
  const [matchboxError, setMatchboxError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    setSnapshotLoading(true);
    const [sys, mb] = await Promise.all([fetchSystem(), fetchMatchbox()]);
    setSnapshot(sys.data);
    setSnapshotError(sys.error);
    setMatchboxHealth(mb.data);
    setMatchboxError(mb.error);
    setSnapshotLoading(false);
  }, [fetchSystem, fetchMatchbox]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const toggles = snapshot?.feature_toggles ?? {};
  const downstream = snapshot?.downstream ?? {};

  // Order the known toggles first, then append any extras. We don't
  // crash if a toggle disappears (stale UI vs newer backend).
  const orderedToggleKeys: string[] = [
    ...TOGGLE_ORDER.filter((k) => k in toggles),
    ...Object.keys(toggles).filter(
      (k) => !(TOGGLE_ORDER as readonly string[]).includes(k),
    ),
  ];

  return (
    <section className="space-y-6" data-testid="settings-view">
      <header className="flex items-start justify-between">
        <div>
          <h1 className="text-xl font-semibold text-gray-900">Settings</h1>
          <p className="text-sm text-gray-600">
            Read-only view of how this deployment is configured. Change values
            via env vars + redeploy.
          </p>
        </div>
        <button
          type="button"
          onClick={refresh}
          disabled={snapshotLoading}
          data-testid="settings-refresh"
          className="rounded border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-800 hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-60"
        >
          {snapshotLoading ? "Loading…" : "Refresh"}
        </button>
      </header>

      {snapshotError ? (
        <p
          role="alert"
          data-testid="settings-fetch-error"
          className="rounded border border-red-200 bg-red-50 p-3 text-sm text-red-700"
        >
          Failed to load system snapshot: {snapshotError}
        </p>
      ) : null}

      {/* ---- Feature toggles ---------------------------------------- */}
      <div
        className="rounded-lg border border-gray-200 bg-white p-4 shadow-sm"
        data-testid="settings-feature-toggles-card"
      >
        <h2 className="text-sm font-semibold uppercase tracking-wide text-gray-600">
          Feature toggles
        </h2>
        {orderedToggleKeys.length === 0 && !snapshotLoading ? (
          <p className="mt-3 text-sm text-gray-600" data-testid="settings-toggles-empty">
            Backend returned no feature_toggles. Probably an old build of
            interface-engine.
          </p>
        ) : null}
        <div className="mt-3 grid grid-cols-1 gap-3 md:grid-cols-2 lg:grid-cols-3">
          {orderedToggleKeys.map((key) => {
            const raw = toggles[key];
            return (
              <ToggleCard
                key={key}
                name={key}
                value={raw}
                description={
                  TOGGLE_DESCRIPTIONS[key] ??
                  "Operator toggle; consult docs/architecture.md for the full reference."
                }
              />
            );
          })}
        </div>
      </div>

      {/* ---- Downstream components ----------------------------------- */}
      <div
        className="rounded-lg border border-gray-200 bg-white p-4 shadow-sm"
        data-testid="settings-downstream-card"
      >
        <h2 className="text-sm font-semibold uppercase tracking-wide text-gray-600">
          Downstream components
        </h2>
        <p className="mt-1 text-xs text-gray-500">
          External systems this deployment talks to. Reachability is shown
          for Matchbox (we have a probe endpoint); HAPI and the auth
          issuer show URL only -- adding probes for those is a follow-up
          (see SettingsView source).
        </p>
        <div className="mt-3 overflow-x-auto">
          <table className="min-w-full divide-y divide-gray-200 text-sm">
            <thead className="bg-gray-50">
              <tr>
                <Th>Component</Th>
                <Th>URL</Th>
                <Th>Status</Th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              <DownstreamRow
                testId="settings-downstream-matchbox"
                label="Matchbox"
                url={asString(downstream.matchbox_base_url)}
              >
                <MatchboxStatusPill
                  health={matchboxHealth}
                  error={matchboxError}
                />
              </DownstreamRow>
              <DownstreamRow
                testId="settings-downstream-hapi"
                label="HAPI FHIR"
                url={asString(downstream.hapi_base_url)}
              >
                {/* v1: no probe -- see comments in SettingsView. */}
                <span
                  data-testid="settings-downstream-hapi-status"
                  className="text-xs text-gray-500"
                >
                  —
                </span>
              </DownstreamRow>
              <DownstreamRow
                testId="settings-downstream-auth"
                label="Auth issuer"
                url={asString(downstream.auth_issuer)}
              >
                <span
                  data-testid="settings-downstream-auth-status"
                  className="text-xs text-gray-500"
                >
                  —
                </span>
              </DownstreamRow>
            </tbody>
          </table>
        </div>
      </div>

      {/* ---- Schema versions ---------------------------------------- */}
      <div
        className="rounded-lg border border-gray-200 bg-white p-4 shadow-sm"
        data-testid="settings-schemas-card"
      >
        <h2 className="text-sm font-semibold uppercase tracking-wide text-gray-600">
          Schema versions
        </h2>
        <div className="mt-3 overflow-x-auto">
          <table className="min-w-full divide-y divide-gray-200 text-sm">
            <thead className="bg-gray-50">
              <tr>
                <Th>Schema</Th>
                <Th>Version</Th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              <tr data-testid="settings-schema-observe">
                <td className="px-3 py-2 text-gray-800">Observe API</td>
                <td className="px-3 py-2 font-mono text-xs text-gray-700">
                  {snapshot?.schema_version ?? "—"}
                </td>
              </tr>
              <tr data-testid="settings-schema-log">
                <td className="px-3 py-2 text-gray-800">
                  Log schema
                  <span
                    className="ml-1 text-xs text-gray-500"
                    title="See docs/observability/log-schema.md. Hardcoded in v1 because no endpoint exposes the log schema version (the schema is enforced by the structured-log emitter, not surfaced over HTTP)."
                  >
                    ⓘ
                  </span>
                </td>
                <td className="px-3 py-2 font-mono text-xs text-gray-700">
                  1.0
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      {/* ---- Build / version info ------------------------------------ */}
      <div
        className="rounded-lg border border-gray-200 bg-white p-4 shadow-sm"
        data-testid="settings-build-card"
      >
        <h2 className="text-sm font-semibold uppercase tracking-wide text-gray-600">
          Build
        </h2>
        <dl className="mt-3 grid grid-cols-2 gap-3 text-sm">
          <div>
            <dt className="text-xs uppercase tracking-wide text-gray-500">
              Service
            </dt>
            <dd
              className="mt-0.5 font-mono text-xs text-gray-800"
              data-testid="settings-build-service"
            >
              {snapshot?.service ?? "—"}
            </dd>
          </div>
          <div>
            <dt className="text-xs uppercase tracking-wide text-gray-500">
              Version
            </dt>
            <dd
              className="mt-0.5 font-mono text-xs text-gray-800"
              data-testid="settings-build-version"
            >
              {snapshot?.version ?? "—"}
            </dd>
          </div>
          <div>
            <dt className="text-xs uppercase tracking-wide text-gray-500">
              Uptime (seconds)
            </dt>
            <dd
              className="mt-0.5 font-mono text-xs text-gray-800"
              data-testid="settings-build-uptime"
            >
              {snapshot?.uptime_seconds ?? "—"}
            </dd>
          </div>
        </dl>
      </div>
    </section>
  );
}

// -- helpers --------------------------------------------------------------

function asString(value: unknown): string {
  if (typeof value === "string" && value.length > 0) return value;
  return "";
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

function ToggleCard({
  name,
  value,
  description,
}: {
  name: string;
  value: unknown;
  description: string;
}) {
  return (
    <div
      className="rounded border border-gray-200 bg-gray-50 p-3"
      data-testid={`settings-toggle-${name}`}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="text-xs font-mono text-gray-700">{name}</div>
        <span
          className="text-xs text-gray-400"
          title={description}
          aria-label={`Description: ${description}`}
        >
          ⓘ
        </span>
      </div>
      <div className="mt-2">
        <TogglePill name={name} value={value} />
      </div>
    </div>
  );
}

function TogglePill({ name, value }: { name: string; value: unknown }) {
  if (typeof value === "boolean") {
    return (
      <span
        data-testid={`settings-toggle-${name}-pill`}
        className={
          value
            ? "inline-flex items-center rounded-full bg-green-100 px-2 py-0.5 text-xs font-medium text-green-800 ring-1 ring-inset ring-green-300"
            : "inline-flex items-center rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-700 ring-1 ring-inset ring-gray-300"
        }
      >
        {value ? "on" : "off"}
      </span>
    );
  }
  if (typeof value === "string" && value.length > 0) {
    return (
      <span
        data-testid={`settings-toggle-${name}-pill`}
        className="inline-flex items-center rounded-full bg-blue-100 px-2 py-0.5 text-xs font-medium text-blue-800 ring-1 ring-inset ring-blue-300"
      >
        {value}
      </span>
    );
  }
  return (
    <span
      data-testid={`settings-toggle-${name}-pill`}
      className="inline-flex items-center rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-700 ring-1 ring-inset ring-gray-300"
    >
      unknown
    </span>
  );
}

function DownstreamRow({
  testId,
  label,
  url,
  children,
}: {
  testId: string;
  label: string;
  url: string;
  children: React.ReactNode;
}) {
  const display = url.length > 0 ? truncate(url, 60) : "—";
  return (
    <tr data-testid={testId}>
      <td className="px-3 py-2 text-gray-800">{label}</td>
      <td
        className="px-3 py-2 font-mono text-xs text-gray-700"
        title={url || undefined}
      >
        {display}
      </td>
      <td className="px-3 py-2">{children}</td>
    </tr>
  );
}

function MatchboxStatusPill({
  health,
  error,
}: {
  health: MatchboxHealth | null;
  error: string | null;
}) {
  if (error) {
    return (
      <span
        data-testid="settings-downstream-matchbox-status-unknown"
        className="inline-flex items-center rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-700 ring-1 ring-inset ring-gray-300"
      >
        unknown
      </span>
    );
  }
  if (!health) {
    return (
      <span
        data-testid="settings-downstream-matchbox-status-loading"
        className="inline-flex items-center rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-700 ring-1 ring-inset ring-gray-300"
      >
        checking…
      </span>
    );
  }
  if (health.reachable) {
    return (
      <span
        data-testid="settings-downstream-matchbox-status-reachable"
        className="inline-flex items-center rounded-full bg-green-100 px-2 py-0.5 text-xs font-medium text-green-800 ring-1 ring-inset ring-green-300"
      >
        reachable
      </span>
    );
  }
  return (
    <span
      data-testid="settings-downstream-matchbox-status-unreachable"
      className="inline-flex items-center rounded-full bg-red-100 px-2 py-0.5 text-xs font-medium text-red-800 ring-1 ring-inset ring-red-300"
    >
      unreachable
    </span>
  );
}

function truncate(value: string, max: number): string {
  if (value.length <= max) return value;
  return value.slice(0, max - 1) + "…";
}
