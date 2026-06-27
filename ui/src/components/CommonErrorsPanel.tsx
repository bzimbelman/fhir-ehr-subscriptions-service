import type { ErrorFingerprintGroup } from "@/lib/dlqTypes";

/**
 * Above-table panel that surfaces the top-N error fingerprints in the
 * currently-loaded DLQ page. Each row is clickable; clicking applies the
 * fingerprint as the `last error pattern` filter so the operator can drill
 * into "show me only the rows that look like THIS".
 *
 * v1 limitation: the grouping is computed client-side over whatever page
 * the API returned (default 50 rows). For larger DLQ backlogs this is
 * representative but not authoritative -- a server-side fingerprint roll-up
 * is a separate story. We document this in the panel so operators know.
 */
interface CommonErrorsPanelProps {
  groups: ErrorFingerprintGroup[];
  /** Total rows considered in the grouping (for the "of N" line). */
  rowCount: number;
  onPickFingerprint: (fingerprint: string) => void;
}

export function CommonErrorsPanel({
  groups,
  rowCount,
  onPickFingerprint,
}: CommonErrorsPanelProps) {
  return (
    <section
      aria-labelledby="common-errors-heading"
      data-testid="common-errors-panel"
      className="rounded-lg border border-gray-200 bg-white p-4 shadow-sm"
    >
      <div className="mb-2 flex items-center justify-between">
        <h2
          id="common-errors-heading"
          className="text-sm font-semibold uppercase tracking-wide text-gray-700"
        >
          Common errors
        </h2>
        <span className="text-xs text-gray-500">
          Top patterns across {rowCount} loaded rows
        </span>
      </div>
      {groups.length === 0 ? (
        <p className="text-sm text-gray-500">No errored rows in this view.</p>
      ) : (
        <ul className="divide-y divide-gray-100">
          {groups.map((g) => (
            <li
              key={g.fingerprint}
              data-testid={`common-error-row-${g.fingerprint}`}
              className="flex items-center gap-3 py-2"
            >
              <button
                type="button"
                onClick={() => onPickFingerprint(g.fingerprint)}
                className="flex-1 truncate text-left font-mono text-xs text-blue-700 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-blue-500"
                title={g.fingerprint}
                aria-label={`Filter by error pattern ${g.fingerprint}`}
              >
                {g.fingerprint}
              </button>
              <span
                data-testid={`common-error-count-${g.fingerprint}`}
                className="rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-800 ring-1 ring-inset ring-gray-200 tabular-nums"
              >
                {g.count}
              </span>
            </li>
          ))}
        </ul>
      )}
      <p className="mt-3 text-xs text-gray-500">
        Fingerprints are computed in the browser from the loaded page; this
        approximates &ldquo;same error&rdquo; without a server-side roll-up
        (deferred to a follow-up).
      </p>
    </section>
  );
}
