import type { SystemStatus } from "@/lib/dashboardTypes";

/**
 * Status pill used in the dashboard top-bar and in component-health rows.
 *
 * Accessibility: we never rely on colour alone -- every pill carries a text
 * label ("UP" / "DEGRADED" / "DOWN" / "UNKNOWN") and `role="status"` so
 * screen readers + colour-blind operators get the same information.
 *
 * Tailwind classes use semantic colours from the default palette; styling
 * is intentionally flat (no gradients, no beads -- see
 * docs/ui-design/reference-screens/README.md for why we're not copying
 * Mirth Connect's visual language).
 */

interface StatusPillProps {
  status: SystemStatus;
  /** Optional override of the visible label (defaults to the status name). */
  label?: string;
  size?: "sm" | "md";
}

const STYLES: Record<SystemStatus, { bg: string; text: string; ring: string }> =
  {
    UP: {
      bg: "bg-green-100",
      text: "text-green-800",
      ring: "ring-green-300",
    },
    DEGRADED: {
      bg: "bg-yellow-100",
      text: "text-yellow-800",
      ring: "ring-yellow-300",
    },
    DOWN: {
      bg: "bg-red-100",
      text: "text-red-800",
      ring: "ring-red-300",
    },
    UNKNOWN: {
      bg: "bg-gray-100",
      text: "text-gray-700",
      ring: "ring-gray-300",
    },
  };

export function StatusPill({
  status,
  label,
  size = "md",
}: StatusPillProps) {
  const s = STYLES[status];
  const padding = size === "sm" ? "px-2 py-0.5 text-xs" : "px-3 py-1 text-sm";
  return (
    <span
      role="status"
      aria-label={`System status ${label ?? status}`}
      data-testid={`status-pill-${status}`}
      className={`inline-flex items-center gap-1.5 rounded-full font-medium ring-1 ring-inset ${s.bg} ${s.text} ${s.ring} ${padding}`}
    >
      <span
        aria-hidden="true"
        className={`h-2 w-2 rounded-full ${
          status === "UP"
            ? "bg-green-500"
            : status === "DEGRADED"
              ? "bg-yellow-500"
              : status === "DOWN"
                ? "bg-red-500"
                : "bg-gray-400"
        }`}
      />
      {label ?? status}
    </span>
  );
}
