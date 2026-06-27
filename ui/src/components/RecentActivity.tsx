import Link from "next/link";
import type { MessageStatus, MessageSummary } from "@/lib/dashboardTypes";
import { relativeTime } from "@/lib/dashboardMetrics";

/**
 * Right column of the two-column section: the last 10 ingested messages.
 *
 * Each row links to `/messages/{id}` which is the placeholder route ticket
 * #402 will replace with the real message-detail viewer. Rendering the link
 * unconditionally is fine -- the destination is part of the agreed
 * information architecture from #399.
 *
 * `source_id` is HL7 MSH-10 and can be unbounded (vendors send absurd
 * strings); we truncate visually but keep the full value in `title` for
 * an operator hovering on the row.
 */

interface RecentActivityProps {
  items: MessageSummary[];
  error?: string | null;
  /** Reference clock for relative time (so tests can pin it). Defaults to now. */
  now?: Date;
}

const STATUS_TONE: Record<MessageStatus, string> = {
  RECEIVED: "bg-blue-100 text-blue-800 ring-blue-300",
  TRANSFORMING: "bg-indigo-100 text-indigo-800 ring-indigo-300",
  DELIVERED: "bg-green-100 text-green-800 ring-green-300",
  FAILED: "bg-yellow-100 text-yellow-800 ring-yellow-300",
  DEAD_LETTER: "bg-red-100 text-red-800 ring-red-300",
};

function truncate(s: string, max = 18): string {
  if (s.length <= max) return s;
  return s.slice(0, max - 1) + "…";
}

export function RecentActivity({ items, error, now }: RecentActivityProps) {
  const refNow = now ?? new Date();
  return (
    <section
      aria-labelledby="recent-activity-heading"
      className="rounded-lg border border-gray-200 bg-white p-4 shadow-sm"
    >
      <h2
        id="recent-activity-heading"
        className="mb-3 text-sm font-semibold uppercase tracking-wide text-gray-700"
      >
        Recent activity
      </h2>
      {error ? (
        <p className="text-sm text-red-700">
          Failed to load recent messages: {error}
        </p>
      ) : items.length === 0 ? (
        <p className="text-sm text-gray-500">No messages yet.</p>
      ) : (
        <ul
          aria-label="Recent ingested messages"
          className="divide-y divide-gray-100"
        >
          {items.map((m) => (
            <li
              key={m.id}
              data-testid={`recent-row-${m.id}`}
              className="py-2 first:pt-0 last:pb-0"
            >
              <Link
                href={`/messages/${m.id}`}
                className="-mx-2 block rounded px-2 py-1 hover:bg-gray-50 focus:outline-none focus-visible:ring-2 focus-visible:ring-blue-500"
              >
                <div className="flex flex-wrap items-center gap-2">
                  <span
                    className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset ${STATUS_TONE[m.status]}`}
                  >
                    {m.status}
                  </span>
                  <span className="text-sm font-medium text-gray-900">
                    {m.source_system}
                  </span>
                  <span
                    className="text-xs font-mono text-gray-500"
                    title={m.source_id}
                  >
                    {truncate(m.source_id)}
                  </span>
                  <span className="text-xs text-gray-700">
                    {m.message_type}
                  </span>
                  <span className="ml-auto text-xs text-gray-500">
                    {relativeTime(m.received_at, refNow)}
                  </span>
                </div>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
