/**
 * Single stat card. Used six times in the stats row at the top of the
 * dashboard. Tries to render zero-state gracefully: when `value` is null we
 * show "--" so the layout doesn't shift between loading and loaded.
 *
 * Information density vs. whitespace: we lean slightly toward density --
 * operators viewing the dashboard from a small ops laptop want to see all
 * six cards in one row. On narrow viewports the cards stack vertically via
 * the parent grid (see StatsRow).
 */

interface StatCardProps {
  label: string;
  value: number | string | null;
  /** Optional sub-line shown beneath the value (e.g., "today", "DLQ"). */
  hint?: string;
  /** Optional accent colour for the value (used for the DLQ count when > 0). */
  tone?: "default" | "warn" | "danger";
}

export function StatCard({ label, value, hint, tone = "default" }: StatCardProps) {
  const toneClass =
    tone === "danger"
      ? "text-red-700"
      : tone === "warn"
        ? "text-yellow-700"
        : "text-gray-900";
  const display = value === null ? "--" : value;
  return (
    <div
      data-testid={`stat-card-${slugify(label)}`}
      className="flex flex-col gap-1 rounded-lg border border-gray-200 bg-white p-4 shadow-sm"
    >
      <span className="text-xs font-medium uppercase tracking-wide text-gray-500">
        {label}
      </span>
      <span className={`text-2xl font-semibold tabular-nums ${toneClass}`}>
        {display}
      </span>
      {hint ? (
        <span className="text-xs text-gray-500">{hint}</span>
      ) : null}
    </div>
  );
}

function slugify(s: string): string {
  return s
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}
