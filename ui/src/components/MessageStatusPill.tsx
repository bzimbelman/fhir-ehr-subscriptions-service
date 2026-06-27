import type { MessageStatus } from "@/lib/dashboardTypes";

/**
 * Coloured pill for a message's pipeline status.
 *
 * The tone palette mirrors [InterfaceDetailView]'s `STATUS_TONE` map, so
 * an operator switching between the per-interface table and the message
 * browser sees the same colour for the same status. Accessibility: every
 * pill carries the text label and `role="status"`; we never rely on
 * colour alone (consistent with the [StatusPill] component).
 */

const TONE: Record<MessageStatus, { bg: string; text: string; ring: string }> =
  {
    RECEIVED: {
      bg: "bg-blue-100",
      text: "text-blue-800",
      ring: "ring-blue-300",
    },
    TRANSFORMING: {
      bg: "bg-indigo-100",
      text: "text-indigo-800",
      ring: "ring-indigo-300",
    },
    DELIVERED: {
      bg: "bg-green-100",
      text: "text-green-800",
      ring: "ring-green-300",
    },
    FAILED: {
      bg: "bg-yellow-100",
      text: "text-yellow-800",
      ring: "ring-yellow-300",
    },
    DEAD_LETTER: {
      bg: "bg-red-100",
      text: "text-red-800",
      ring: "ring-red-300",
    },
  };

interface MessageStatusPillProps {
  status: MessageStatus;
  size?: "sm" | "md";
}

export function MessageStatusPill({
  status,
  size = "sm",
}: MessageStatusPillProps) {
  const tone = TONE[status];
  const padding = size === "sm" ? "px-2 py-0.5 text-xs" : "px-3 py-1 text-sm";
  return (
    <span
      role="status"
      aria-label={`Message status ${status}`}
      data-testid={`message-status-pill-${status}`}
      className={`inline-flex items-center rounded-full font-medium ring-1 ring-inset ${tone.bg} ${tone.text} ${tone.ring} ${padding}`}
    >
      {status}
    </span>
  );
}
