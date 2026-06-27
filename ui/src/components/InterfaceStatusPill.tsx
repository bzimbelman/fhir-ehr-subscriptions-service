import type { InterfaceStatus } from "@/lib/interfaces";

/**
 * Pill for per-interface status (active / idle / error / quiet).
 *
 * Distinct from `StatusPill` (which surfaces a SystemStatus -- UP/DEGRADED/
 * DOWN/UNKNOWN). The Mirth-equivalent "channel state" lives at a different
 * level of abstraction than "matchbox is up" so we reach for a separate
 * component rather than overloading the existing one.
 *
 * Colour mapping (chosen so the worst news is the most attention-grabbing):
 *   active -> green   (healthy, recent traffic)
 *   idle   -> blue    (no traffic recently, not broken)
 *   error  -> red     (recent failures dominate)
 *   quiet  -> grey    (no traffic at all in 7d)
 */

interface InterfaceStatusPillProps {
  status: InterfaceStatus;
  size?: "sm" | "md";
}

const STYLES: Record<
  InterfaceStatus,
  { bg: string; text: string; ring: string; dot: string }
> = {
  active: {
    bg: "bg-green-100",
    text: "text-green-800",
    ring: "ring-green-300",
    dot: "bg-green-500",
  },
  idle: {
    bg: "bg-blue-100",
    text: "text-blue-800",
    ring: "ring-blue-300",
    dot: "bg-blue-500",
  },
  error: {
    bg: "bg-red-100",
    text: "text-red-800",
    ring: "ring-red-300",
    dot: "bg-red-500",
  },
  quiet: {
    bg: "bg-gray-100",
    text: "text-gray-700",
    ring: "ring-gray-300",
    dot: "bg-gray-400",
  },
};

export function InterfaceStatusPill({
  status,
  size = "md",
}: InterfaceStatusPillProps) {
  const s = STYLES[status];
  const padding = size === "sm" ? "px-2 py-0.5 text-xs" : "px-3 py-1 text-sm";
  return (
    <span
      role="status"
      aria-label={`Interface status ${status}`}
      data-testid={`interface-status-${status}`}
      className={`inline-flex items-center gap-1.5 rounded-full font-medium ring-1 ring-inset ${s.bg} ${s.text} ${s.ring} ${padding}`}
    >
      <span aria-hidden="true" className={`h-2 w-2 rounded-full ${s.dot}`} />
      {status}
    </span>
  );
}
