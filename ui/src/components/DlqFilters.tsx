import {
  DEFAULT_FILTERS,
  SOURCE_PROTOCOLS,
  TIME_RANGES,
  type DlqFilters as DlqFiltersState,
  type SourceProtocol,
  type TimeRange,
} from "@/lib/dlqTypes";

/**
 * Filter bar for the DLQ list. Controlled component -- parent owns the
 * filter state so it can also be driven by the "Common errors" panel
 * (clicking a fingerprint sets the lastErrorPattern field).
 */
interface DlqFiltersProps {
  value: DlqFiltersState;
  onChange: (next: DlqFiltersState) => void;
}

const TIME_RANGE_LABELS: Record<TimeRange, string> = {
  "1h": "Last hour",
  "24h": "Last 24 hours",
  "7d": "Last 7 days",
  all: "All time",
};

export function DlqFilters({ value, onChange }: DlqFiltersProps) {
  const set = (patch: Partial<DlqFiltersState>) =>
    onChange({ ...value, ...patch });

  return (
    <section
      aria-labelledby="dlq-filters-heading"
      data-testid="dlq-filters"
      className="rounded-lg border border-gray-200 bg-white p-4 shadow-sm"
    >
      <div className="mb-3 flex items-center justify-between">
        <h2
          id="dlq-filters-heading"
          className="text-sm font-semibold uppercase tracking-wide text-gray-700"
        >
          Filters
        </h2>
        <button
          type="button"
          onClick={() => onChange(DEFAULT_FILTERS)}
          data-testid="filters-reset"
          className="text-xs text-blue-700 hover:underline"
        >
          Reset
        </button>
      </div>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-5">
        <label className="flex flex-col text-xs text-gray-700">
          Source system
          <input
            type="text"
            value={value.sourceSystem}
            onChange={(e) => set({ sourceSystem: e.target.value })}
            data-testid="filter-source-system"
            className="mt-1 rounded border border-gray-300 px-2 py-1 text-sm"
            placeholder="contains&hellip;"
          />
        </label>
        <label className="flex flex-col text-xs text-gray-700">
          Source protocol
          <select
            value={value.sourceProtocol}
            onChange={(e) =>
              set({
                sourceProtocol: e.target.value as SourceProtocol | "all",
              })
            }
            data-testid="filter-source-protocol"
            className="mt-1 rounded border border-gray-300 bg-white px-2 py-1 text-sm"
          >
            <option value="all">All protocols</option>
            {SOURCE_PROTOCOLS.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </select>
        </label>
        <label className="flex flex-col text-xs text-gray-700">
          Message type
          <input
            type="text"
            value={value.messageType}
            onChange={(e) => set({ messageType: e.target.value })}
            data-testid="filter-message-type"
            className="mt-1 rounded border border-gray-300 px-2 py-1 text-sm"
            placeholder="contains&hellip;"
          />
        </label>
        <label className="flex flex-col text-xs text-gray-700">
          Time range
          <select
            value={value.timeRange}
            onChange={(e) =>
              set({ timeRange: e.target.value as TimeRange })
            }
            data-testid="filter-time-range"
            className="mt-1 rounded border border-gray-300 bg-white px-2 py-1 text-sm"
          >
            {TIME_RANGES.map((r) => (
              <option key={r} value={r}>
                {TIME_RANGE_LABELS[r]}
              </option>
            ))}
          </select>
        </label>
        <label className="flex flex-col text-xs text-gray-700">
          Last error pattern
          <input
            type="text"
            value={value.lastErrorPattern}
            onChange={(e) => set({ lastErrorPattern: e.target.value })}
            data-testid="filter-last-error"
            className="mt-1 rounded border border-gray-300 px-2 py-1 text-sm font-mono"
            placeholder="contains&hellip;"
          />
        </label>
      </div>
    </section>
  );
}
