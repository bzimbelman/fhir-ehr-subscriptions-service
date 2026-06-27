# 04 — Message Browser

> Mirth Connect "Messages" tab on a channel, OR the global Messages search
> view. The "find one specific message that flowed through" tool.

## What this screen shows in Mirth

The Message Browser is a three-pane layout:

```
┌──────────────┬─────────────────────────────────────────┐
│              │                                         │
│  FILTERS     │           MESSAGE LIST                  │
│  (left)      │           (top right)                   │
│              │                                         │
│  [time range]│  ┌──┬────────────┬─────────┬──────────┐ │
│  [status]    │  │# │ Received   │ Status  │ Errors   │ │
│  [conn type] │  ├──┼────────────┼─────────┼──────────┤ │
│  [content    │  │1 │ 11:02:33   │ SENT    │ 0        │ │
│   search]    │  │2 │ 11:02:34   │ ERROR   │ 1        │ │
│  [advanced]  │  │  │ ...        │ ...     │          │ │
│              │  └──┴────────────┴─────────┴──────────┘ │
│              ├─────────────────────────────────────────┤
│              │           MESSAGE DETAIL                │
│              │           (bottom right)                │
│              │   [tabs: Raw | Processed Raw |          │
│              │    Transformed | Encoded | Sent |       │
│              │    Response | Mappings | Errors ]       │
│              │                                         │
└──────────────┴─────────────────────────────────────────┘
```

The **left filter panel** has:

- Time range (preset buttons: Last hour / Today / Last 7 days / Custom)
- Status checkboxes: Received, Filtered, Queued, Sent, Errored, Pending
- Connector type filter (Source / specific Destination)
- Free-text content search across raw payload
- An "Advanced Search" button that opens a modal with field-level matchers
  (e.g. `PID-3 = "12345"` for HL7 patient ID)
- "Quick Search" by message ID

The **message list** is a scrollable virtualized table sorted by received-time
desc. Each row shows: sequence #, timestamp, status badge, error count,
connector name. Clicking a row populates the **detail panel** at the bottom.

The detail panel uses a tab strip across multiple **transformation stages** —
in Mirth's pipeline a message moves through Raw → Filtered → Transformed →
Encoded → Sent → Response, and each stage can be inspected.

## What we're adapting

A **Messages search page** (`/messages`) with the same three-pane shape:

- **Left**: filter sidebar. Time range, status (Received / Delivered /
  Failed / Pending), interface, subscription, free-text content search,
  message-ID lookup.
- **Top right**: virtualized list with one row per message, sortable by
  received time, status colour-coded.
- **Bottom right** (or as a slide-over panel on smaller screens): selected
  message detail.

The filter-list-detail pattern is the right shape because operators search
for messages **starting from a complaint** ("a result didn't show up at 3pm")
— so a fast time-bounded search with status filtering is the primary task.

## What we're explicitly NOT copying

- The dense, no-padding table. Use generous row height + monospace timestamps.
- The 8-tab detail panel. Our pipeline is simpler (Raw → Parsed →
  Delivered/Errored) — see `05-message-detail.md` for the simplified tab
  scheme.
- The "Advanced Search" modal popup. Field-level matchers should be inline
  pill-style filters that the user can stack, not a separate dialog.
- Hard-to-find sort: sort headers should be visibly clickable, not
  right-click context menus as in Swing.
- The Swing-style chrome (grey panels, beveled splitter bars). Use a clean
  splitter or a flex layout.

## Which ticket implements our equivalent

**#402 (UI: message viewer — search, drill-down, raw + parsed view).**
Secondary readers: #403 (DLQ uses a similar filter+list shape, narrower
scope) and #404 (subscription delivery history — a list-of-attempts view
that mirrors this layout per subscription).
