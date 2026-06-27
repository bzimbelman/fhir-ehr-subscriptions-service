import type { AgeBand } from "@/lib/dlqUtils";

/**
 * Small inline pill that summarises a row's age band. Lives next to the
 * "received at" column in the DLQ list. Colour-coded for at-a-glance triage;
 * label text is always shown so colour-blind operators get the same info.
 *
 * Thresholds (defined in dlqUtils.ageBand):
 *   green  < 1 h
 *   yellow 1-24 h
 *   red    > 24 h or unknown
 */
interface AgeBadgeProps {
  band: AgeBand;
  /** Human label like "12 min ago" -- shown next to the swatch. */
  label: string;
}

const STYLES: Record<AgeBand, string> = {
  green: "bg-green-100 text-green-800 ring-green-300",
  yellow: "bg-yellow-100 text-yellow-800 ring-yellow-300",
  red: "bg-red-100 text-red-800 ring-red-300",
};

export function AgeBadge({ band, label }: AgeBadgeProps) {
  return (
    <span
      data-testid={`age-badge-${band}`}
      className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset ${STYLES[band]}`}
    >
      <span
        aria-hidden="true"
        className={`h-1.5 w-1.5 rounded-full ${
          band === "green"
            ? "bg-green-500"
            : band === "yellow"
              ? "bg-yellow-500"
              : "bg-red-500"
        }`}
      />
      {label}
    </span>
  );
}
