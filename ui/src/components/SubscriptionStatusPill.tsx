import type { SubscriptionStatusCode } from "@/lib/subscriptionTypes";

/**
 * Status pill for FHIR Subscription.status values (ticket #404).
 *
 * Separate from the system-status `StatusPill` (which uses UP /
 * DEGRADED / DOWN / UNKNOWN) because the vocab is different:
 *   - `active`    — green   (HAPI accepted + is delivering)
 *   - `off`       — gray    (operator-disabled or HAPI-stopped)
 *   - `requested` — yellow  (registered but not yet activated)
 *   - `error`     — red     (last delivery failed; HAPI flipped the status)
 *
 * Accessibility: never colour-only — every pill carries the FHIR
 * status code as visible text and `role="status"` for screen readers.
 *
 * Reuses the visual treatment from the dashboard pill so the operator
 * console feels cohesive across pages.
 */

interface Props {
  /** The FHIR R4 Subscription.status code, lowercase. */
  status: SubscriptionStatusCode | string;
}

const TONES: Record<string, { bg: string; text: string; ring: string; dot: string }> = {
  active: {
    bg: "bg-green-100",
    text: "text-green-800",
    ring: "ring-green-300",
    dot: "bg-green-500",
  },
  off: {
    bg: "bg-gray-100",
    text: "text-gray-700",
    ring: "ring-gray-300",
    dot: "bg-gray-400",
  },
  requested: {
    bg: "bg-yellow-100",
    text: "text-yellow-800",
    ring: "ring-yellow-300",
    dot: "bg-yellow-500",
  },
  error: {
    bg: "bg-red-100",
    text: "text-red-800",
    ring: "ring-red-300",
    dot: "bg-red-500",
  },
};

const UNKNOWN_TONE = {
  bg: "bg-gray-100",
  text: "text-gray-700",
  ring: "ring-gray-300",
  dot: "bg-gray-400",
};

export function SubscriptionStatusPill({ status }: Props) {
  const tone = TONES[status] ?? UNKNOWN_TONE;
  return (
    <span
      role="status"
      data-testid={`subscription-pill-${status}`}
      aria-label={`Subscription status ${status}`}
      className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium ring-1 ring-inset ${tone.bg} ${tone.text} ${tone.ring}`}
    >
      <span aria-hidden="true" className={`h-1.5 w-1.5 rounded-full ${tone.dot}`} />
      {status}
    </span>
  );
}
