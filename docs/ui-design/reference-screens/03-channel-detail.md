# 03 — Channel Detail (statistics + recent messages)

> Mirth Connect dashboard → expand a channel row, OR Channels → double-click a
> deployed channel. The drill-down view for a single channel.

## What this screen shows in Mirth

When you expand a channel row on the Dashboard (clicking the "+" icon to its
left), an inline panel slides open underneath the row revealing **per-channel
statistics broken out by Source and each Destination**:

```
  Channel: HL7-ADT-to-FHIR-Patient
  ├── Source (LLP Listener)        Received: 12,340   Filtered: 12       Queued:   0   Errored: 3
  ├── Destination 1: HTTP Sender   Sent: 12,300       Errored: 25                       Queued: 12
  └── Destination 2: File Writer   Sent: 12,328       Errored: 0                        Queued: 0
```

Each row shows the same counter set as the Dashboard, but scoped to that
connector. There's a small **Show Messages** button per connector that opens
the Message Browser pre-filtered to that destination.

If you instead open the channel by double-click, you land on a channel-detail
window with three tabs:

1. **Summary** — small set of fields: name, description, deploy state, source
   connector type, destination count.
2. **Statistics** — full historical message counters with reset buttons.
3. **Messages** — embedded Message Browser pre-filtered to this channel
   (this is the actual usable tab).

A footer bar shows: last received timestamp, last sent timestamp, queue depth
across all destinations, "Connection State" per connector (Idle / Connected /
Polling / Disconnected with a reason string).

## What we're adapting

The **per-Interface page** (`/interfaces/[id]`) is the right home for:

- A header card with: name, source system (e.g. "Epic ADT feed"), state
  badge, last activity timestamp, total queue depth
- A **breakdown by destination/subscription** — each subscription gets its
  own row with: Sent, Errored, Pending, last delivered timestamp, link to
  this subscription's history (which is #404's screen)
- A "Recent Messages" embedded list — last ~50 messages this Interface has
  seen, time-ordered desc, with a "View all → message browser pre-filtered"
  link
- Source-side metrics: receive rate (msg/min over last 5/15/60 min), inbound
  connection state, last received time
- A small set of operator actions: Pause, Resume, Drain queue, Replay last N

The pattern of "open this screen and within 5 seconds know if this interface
is healthy" is exactly what Mirth's drill-down gets right.

## What we're explicitly NOT copying

- The triple-tab layout (Summary / Statistics / Messages). We collapse it to
  one page with sections.
- The "Reset Statistics" buttons. Our counters live in Prometheus, scoped to
  windows — no manual reset.
- The inline "expand row" interaction on the dashboard. We use a navigation
  click. Expanding rows is fiddly and doesn't scale to deep links.
- The "Channel Editor" launched from this view. Out of scope for v1.
- Per-destination "Reprocess Queued" button — replay actions live in the DLQ
  screen (#403), not here.

## Which ticket implements our equivalent

**#401 (UI: per-interface state and stats).** Secondary readers: #400 for
the "embedded recent-messages" pattern, and #404 for the per-subscription
delivery-log linked from this page.
