import type { ComponentHealthRow } from "@/lib/dashboardTypes";
import { StatusPill } from "@/components/StatusPill";

/**
 * Left column of the two-column section: a list of platform components and
 * their current health. The data comes from `/admin/observe/system` (via
 * `componentHealthRows` in dashboardMetrics). When the backend reports zero
 * components (older versions), we render a clear "no component telemetry
 * exposed by backend" message rather than a misleading "all healthy" --
 * absence of evidence is not evidence of UP.
 */
interface ComponentHealthListProps {
  rows: ComponentHealthRow[];
  error?: string | null;
}

export function ComponentHealthList({
  rows,
  error,
}: ComponentHealthListProps) {
  return (
    <section
      aria-labelledby="component-health-heading"
      className="rounded-lg border border-gray-200 bg-white p-4 shadow-sm"
    >
      <h2
        id="component-health-heading"
        className="mb-3 text-sm font-semibold uppercase tracking-wide text-gray-700"
      >
        Component health
      </h2>
      {error ? (
        <p className="text-sm text-red-700">
          Failed to load component health: {error}
        </p>
      ) : rows.length === 0 ? (
        <p className="text-sm text-gray-500">
          Backend exposes no per-component health on{" "}
          <code className="font-mono">/admin/observe/system</code> yet. Upgrade
          the interface-engine to surface matchbox / hapi / postgres status here.
        </p>
      ) : (
        <ul className="divide-y divide-gray-100">
          {rows.map((row) => (
            <li
              key={row.name}
              className="flex items-center justify-between gap-3 py-2"
              data-testid={`component-health-row-${row.name}`}
            >
              <div className="flex flex-col">
                <span className="text-sm font-medium text-gray-900">
                  {row.name}
                </span>
                {row.detail ? (
                  <span className="text-xs text-gray-500">{row.detail}</span>
                ) : null}
              </div>
              <div className="flex items-center gap-2">
                <StatusPill status={row.status} size="sm" />
                {row.lastChecked ? (
                  <span className="text-xs text-gray-500">
                    {row.lastChecked}
                  </span>
                ) : null}
              </div>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
