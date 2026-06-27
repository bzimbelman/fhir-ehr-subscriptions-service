"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  fetchMatchboxHealth,
  fetchStructureMaps,
  runMatchboxTransform,
  SAMPLE_ADT_A04,
  type ApiResult,
} from "@/lib/matchboxClient";
import type {
  MatchboxHealth,
  StructureMapsEnvelope,
  TransformResponse,
  TransformRequest,
} from "@/lib/matchboxTypes";

/**
 * Operator Matchbox transform inspector (Epic #398, ticket #405).
 *
 * Three vertical sections:
 *
 *   1. Health card  - is Matchbox reachable? version + base URL + last check
 *   2. StructureMaps table - what maps are loaded, with a text filter
 *   3. Try a transform - paste HL7 v2, see the resulting Bundle
 *
 * All three speak to the backend through the proxy at
 * `/api/admin/matchbox/...`; the admin-API bearer token never reaches
 * the browser (same model as the rest of the operator UI).
 */

interface MatchboxViewProps {
  /** Test seam: a fake health fetcher replacing the real network call. */
  fetchHealth?: () => Promise<ApiResult<MatchboxHealth>>;
  /** Test seam: a fake structuremaps fetcher. */
  fetchMaps?: () => Promise<ApiResult<StructureMapsEnvelope>>;
  /** Test seam: a fake transform call. */
  runTransform?: (body: TransformRequest) => Promise<ApiResult<TransformResponse>>;
  /** Reference clock for the "checked Xs ago" label (deterministic tests). */
  nowProvider?: () => Date;
}

export function MatchboxView({
  fetchHealth = fetchMatchboxHealth,
  fetchMaps = fetchStructureMaps,
  runTransform = runMatchboxTransform,
  nowProvider,
}: MatchboxViewProps) {
  const [health, setHealth] = useState<MatchboxHealth | null>(null);
  const [healthError, setHealthError] = useState<string | null>(null);
  const [healthLoading, setHealthLoading] = useState<boolean>(true);

  const [maps, setMaps] = useState<StructureMapsEnvelope | null>(null);
  const [mapsError, setMapsError] = useState<string | null>(null);

  const [filter, setFilter] = useState<string>("");

  const [rawMessage, setRawMessage] = useState<string>("");
  const [mapUrl, setMapUrl] = useState<string>("");
  const [transformResult, setTransformResult] = useState<TransformResponse | null>(
    null,
  );
  const [transformError, setTransformError] = useState<string | null>(null);
  const [transforming, setTransforming] = useState<boolean>(false);

  const healthAbortRef = useRef<AbortController | null>(null);

  const refreshHealth = useCallback(async () => {
    healthAbortRef.current?.abort();
    const ctrl = new AbortController();
    healthAbortRef.current = ctrl;
    setHealthLoading(true);
    const result = await fetchHealth();
    if (ctrl.signal.aborted) return;
    setHealth(result.data);
    setHealthError(result.error);
    setHealthLoading(false);
  }, [fetchHealth]);

  const refreshMaps = useCallback(async () => {
    const result = await fetchMaps();
    setMaps(result.data);
    setMapsError(result.error);
  }, [fetchMaps]);

  // Initial load: health + structuremaps in parallel. We do NOT
  // auto-refresh - this is an inspector surface, not a dashboard, and
  // re-polling matchbox to "see if it changed" without operator
  // intent would only add noise.
  useEffect(() => {
    refreshHealth();
    refreshMaps();
    return () => {
      healthAbortRef.current?.abort();
    };
  }, [refreshHealth, refreshMaps]);

  const refNow = nowProvider ? nowProvider() : new Date();

  const onTransform = useCallback(async () => {
    setTransforming(true);
    setTransformError(null);
    setTransformResult(null);
    const body: TransformRequest = {
      source_format: "hl7v2",
      raw_message: rawMessage,
    };
    if (mapUrl.trim().length > 0) body.map_url = mapUrl.trim();
    const result = await runTransform(body);
    setTransformResult(result.data);
    setTransformError(result.error);
    setTransforming(false);
  }, [rawMessage, mapUrl, runTransform]);

  const onTrySample = useCallback(() => {
    setRawMessage(SAMPLE_ADT_A04);
  }, []);

  const visibleMaps = useMemo(() => {
    const items = maps?.items ?? [];
    const f = filter.trim().toLowerCase();
    if (!f) return items;
    return items.filter((row) => {
      return (
        (row.name?.toLowerCase().includes(f) ?? false) ||
        (row.title?.toLowerCase().includes(f) ?? false) ||
        (row.url?.toLowerCase().includes(f) ?? false) ||
        row.id.toLowerCase().includes(f)
      );
    });
  }, [maps, filter]);

  return (
    <section className="space-y-6" data-testid="matchbox-view">
      <header>
        <h1 className="text-xl font-semibold text-gray-900">Matchbox</h1>
        <p className="text-sm text-gray-600">
          FHIR mapping engine inspector. View health, loaded StructureMaps,
          and run an interactive transform.
        </p>
      </header>

      {/* ---- Health card -------------------------------------------- */}
      <div
        className="rounded-lg border border-gray-200 bg-white p-4 shadow-sm"
        data-testid="matchbox-health-card"
      >
        <div className="flex items-center justify-between gap-3">
          <h2 className="text-sm font-semibold uppercase tracking-wide text-gray-600">
            Health
          </h2>
          <button
            type="button"
            onClick={refreshHealth}
            disabled={healthLoading}
            data-testid="matchbox-health-refresh"
            className="rounded border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-800 hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-60"
          >
            {healthLoading ? "Checking…" : "Refresh"}
          </button>
        </div>
        <div className="mt-3 flex flex-wrap items-center gap-4 text-sm">
          <HealthPill loading={healthLoading} health={health} error={healthError} />
          <div className="text-gray-700">
            <span className="text-gray-500">Version:</span>{" "}
            <span className="font-mono" data-testid="matchbox-health-version">
              {health?.version ?? "—"}
            </span>
          </div>
          <div
            className="font-mono text-xs text-gray-700"
            data-testid="matchbox-health-base-url"
            title={health?.base_url ?? undefined}
          >
            {truncate(health?.base_url ?? "—", 50)}
          </div>
          <div className="text-xs text-gray-500" data-testid="matchbox-health-checked-at">
            {health?.checked_at
              ? `Checked ${relativeTime(health.checked_at, refNow)}`
              : ""}
          </div>
          <div
            className="text-xs text-gray-500"
            data-testid="matchbox-health-response-time"
          >
            {typeof health?.response_time_ms === "number"
              ? `${health.response_time_ms} ms`
              : ""}
          </div>
        </div>
        {health?.error ? (
          <p
            role="alert"
            data-testid="matchbox-health-error"
            className="mt-3 rounded border border-red-200 bg-red-50 p-2 text-xs text-red-700"
          >
            {health.error}
          </p>
        ) : null}
        {healthError ? (
          <p
            role="alert"
            data-testid="matchbox-health-fetch-error"
            className="mt-3 rounded border border-red-200 bg-red-50 p-2 text-xs text-red-700"
          >
            Health fetch failed: {healthError}
          </p>
        ) : null}
      </div>

      {/* ---- StructureMaps table ------------------------------------ */}
      <div
        className="rounded-lg border border-gray-200 bg-white p-4 shadow-sm"
        data-testid="matchbox-structuremaps-card"
      >
        <div className="flex items-center justify-between gap-3">
          <h2 className="text-sm font-semibold uppercase tracking-wide text-gray-600">
            StructureMaps
          </h2>
          <input
            type="text"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder="Filter by name / title / url"
            data-testid="matchbox-structuremaps-filter"
            className="w-72 rounded border border-gray-300 bg-white px-2 py-1 text-sm"
          />
        </div>

        {health && health.reachable === false ? (
          <p
            role="alert"
            data-testid="matchbox-structuremaps-unreachable"
            className="mt-3 rounded border border-yellow-200 bg-yellow-50 p-3 text-sm text-yellow-800"
          >
            Matchbox unreachable — health check failed at{" "}
            {health.checked_at}
            {health.error ? `: ${health.error}` : ""}
          </p>
        ) : null}

        {mapsError ? (
          <p
            role="alert"
            data-testid="matchbox-structuremaps-fetch-error"
            className="mt-3 rounded border border-red-200 bg-red-50 p-2 text-xs text-red-700"
          >
            StructureMaps fetch failed: {mapsError}
          </p>
        ) : null}

        {maps && maps.total > 0 ? (
          <div className="mt-3 overflow-x-auto">
            <table className="min-w-full divide-y divide-gray-200 text-sm">
              <thead className="bg-gray-50">
                <tr>
                  <Th>ID</Th>
                  <Th>URL</Th>
                  <Th>Name</Th>
                  <Th>Title</Th>
                  <Th>Status</Th>
                  <Th>Version</Th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100">
                {visibleMaps.map((row) => (
                  <tr
                    key={row.id}
                    data-testid={`matchbox-sm-row-${row.id}`}
                    className="hover:bg-gray-50"
                  >
                    <td className="px-3 py-2 font-mono text-xs">{row.id}</td>
                    <td
                      className="px-3 py-2 font-mono text-xs text-gray-700"
                      title={row.url ?? undefined}
                    >
                      {truncate(row.url ?? "—", 50)}
                    </td>
                    <td className="px-3 py-2 text-gray-800">{row.name ?? "—"}</td>
                    <td className="px-3 py-2 text-gray-700">{row.title ?? "—"}</td>
                    <td className="px-3 py-2 text-gray-700">{row.status ?? "—"}</td>
                    <td className="px-3 py-2 text-gray-700">{row.version ?? "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            <p className="mt-2 text-xs text-gray-500">
              {visibleMaps.length} of {maps.total} StructureMaps
            </p>
          </div>
        ) : null}

        {maps && maps.total === 0 && !mapsError && !(health && health.reachable === false) ? (
          <p
            data-testid="matchbox-structuremaps-empty"
            className="mt-3 text-sm text-gray-600"
          >
            No StructureMaps loaded.
          </p>
        ) : null}
      </div>

      {/* ---- Try a transform ---------------------------------------- */}
      <div
        className="rounded-lg border border-gray-200 bg-white p-4 shadow-sm"
        data-testid="matchbox-transform-card"
      >
        <h2 className="text-sm font-semibold uppercase tracking-wide text-gray-600">
          Try a transform
        </h2>
        <p className="mt-1 text-xs text-gray-500">
          Paste an HL7 v2 message; the server runs Matchbox{" "}
          <code className="rounded bg-gray-100 px-1 py-0.5 font-mono">$transform</code>{" "}
          and returns the resulting FHIR Bundle.
        </p>

        <div className="mt-3 space-y-2">
          <label className="block text-xs font-medium text-gray-700">
            HL7 v2 message
            <textarea
              value={rawMessage}
              onChange={(e) => setRawMessage(e.target.value)}
              rows={8}
              data-testid="matchbox-transform-input"
              placeholder="MSH|^~\&|..."
              className="mt-1 block w-full rounded border border-gray-300 bg-white p-2 font-mono text-xs"
            />
          </label>
          <label className="block text-xs font-medium text-gray-700">
            StructureMap URL (optional)
            <input
              type="text"
              value={mapUrl}
              onChange={(e) => setMapUrl(e.target.value)}
              data-testid="matchbox-transform-map-url"
              placeholder="Leave blank to use server default"
              className="mt-1 block w-full rounded border border-gray-300 bg-white px-2 py-1 font-mono text-xs"
            />
          </label>
          <div className="flex items-center gap-3">
            <button
              type="button"
              onClick={onTransform}
              disabled={transforming || rawMessage.trim().length === 0}
              data-testid="matchbox-transform-run"
              className="rounded bg-blue-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-700 disabled:cursor-not-allowed disabled:opacity-60"
            >
              {transforming ? "Transforming…" : "Transform"}
            </button>
            <button
              type="button"
              onClick={onTrySample}
              data-testid="matchbox-transform-sample"
              className="rounded border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-800 hover:bg-gray-100"
            >
              Try sample
            </button>
          </div>
        </div>

        {transformError ? (
          <p
            role="alert"
            data-testid="matchbox-transform-fetch-error"
            className="mt-3 rounded border border-red-200 bg-red-50 p-2 text-xs text-red-700"
          >
            Transform request failed: {transformError}
          </p>
        ) : null}

        {transformResult ? (
          transformResult.success ? (
            <pre
              data-testid="matchbox-transform-output"
              className="mt-3 max-h-96 overflow-auto rounded border border-gray-200 bg-gray-50 p-3 font-mono text-xs"
            >
              {JSON.stringify(transformResult.bundle, null, 2)}
            </pre>
          ) : (
            <p
              role="alert"
              data-testid="matchbox-transform-error"
              className="mt-3 rounded border border-red-200 bg-red-50 p-3 text-sm text-red-700"
            >
              Matchbox returned an error:{" "}
              <span className="font-mono">{transformResult.error}</span>
            </p>
          )
        ) : null}
      </div>
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

function HealthPill({
  loading,
  health,
  error,
}: {
  loading: boolean;
  health: MatchboxHealth | null;
  error: string | null;
}) {
  if (loading && !health) {
    return (
      <span
        data-testid="matchbox-health-pill-checking"
        className="inline-flex items-center rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-700 ring-1 ring-inset ring-gray-300"
      >
        checking…
      </span>
    );
  }
  if (error || !health) {
    return (
      <span
        data-testid="matchbox-health-pill-unknown"
        className="inline-flex items-center rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-700 ring-1 ring-inset ring-gray-300"
      >
        unknown
      </span>
    );
  }
  if (health.reachable) {
    return (
      <span
        data-testid="matchbox-health-pill-healthy"
        className="inline-flex items-center rounded-full bg-green-100 px-2 py-0.5 text-xs font-medium text-green-800 ring-1 ring-inset ring-green-300"
      >
        healthy
      </span>
    );
  }
  return (
    <span
      data-testid="matchbox-health-pill-unreachable"
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

function relativeTime(iso: string, refNow: Date): string {
  const then = new Date(iso).getTime();
  const now = refNow.getTime();
  if (!Number.isFinite(then)) return iso;
  const seconds = Math.max(0, Math.floor((now - then) / 1000));
  if (seconds < 5) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}
