# 06 — Errored Messages (DLQ equivalent)

> Mirth Connect "Message Browser with Status = Errored" filter. There is no
> dedicated DLQ screen in Mirth; the errored-message view is the closest thing.

## What this screen shows in Mirth

In Mirth, "errored messages" are surfaced as a filter on the standard Message
Browser (`04-message-browser.md`) — you uncheck Sent/Filtered/Queued and
leave only Errored. The list then shows:

- All messages where at least one destination errored
- Sorted desc by error timestamp by default
- Columns: sequence #, received timestamp, channel, connector that errored,
  error class (e.g. `ConnectException`, `JavaScriptException`), brief error
  string

Above the list is a small toolbar with the actions that distinguish this from
a regular browse:

- **Reprocess** — re-runs the entire message through the channel pipeline,
  starting at the source connector. Effectively "replay from scratch."
- **Send** — re-sends just to the destination that errored, skipping the
  source/transformer stages. (Confusing — operators frequently pick the
  wrong one.)
- **Remove** — delete the message from history (rare; usually retention does
  this).
- **Mark as Processed** — clears the error status without re-running anything,
  used when an operator has manually resolved out-of-band.

Selecting a row populates the same bottom detail panel as `05-message-detail.md`,
with the Errors tab opened by default.

The list is **not aggregated** — if the same upstream message keeps failing
the same way, you see one row per failure attempt. Mirth has no built-in
"group identical errors" view.

## What we're adapting

Our **DLQ view** at `/dlq` (or `/errors`) is a first-class screen, not a
filter on Messages. It should:

- Show a list of failed deliveries, with optional grouping by error
  fingerprint (same error class + same destination + last hour) so an
  operator can see "23 failures of the same type" rather than 23 individual
  rows. Group expand-collapses to show individual entries.
- Per-row: timestamp of failure, source interface, subscription that failed,
  HTTP status / error class, brief error message (truncated), retry count
- Provide a single, unambiguous **Replay** action per row (and on selected
  rows in bulk) — no "Reprocess vs Send" confusion. Replay always means "try
  to deliver this again from the parsed payload."
- Provide a **Discard** action — explicitly remove from DLQ; logged in audit
  trail (#407).
- Filterable by interface, subscription, error class, time range.
- Each row drills into the same Message Detail view (#402) with the Errors
  tab open.

## What we're explicitly NOT copying

- The "Reprocess vs Send" duality. We have **one** Replay action with clear
  semantics ("retry delivery to the failed subscription[s] using the parsed
  payload we already have").
- The "Mark as Processed" no-op action. Either you Replay it (and it
  succeeds, or fails again), or you Discard it. There's no in-between.
- The "errors are a filter on messages" framing. DLQ is its own page in our
  product because it's a different operator workflow (the workflow that
  follows the page on-call sees in PagerDuty).
- The flat, ungrouped error list. Grouping by error fingerprint is the
  single biggest improvement we can make over Mirth.

## Which ticket implements our equivalent

**#403 (UI: DLQ viewer with replay + delete).** Secondary readers: #404
(subscription delivery history — shares the "row-per-failed-attempt" shape)
and #407 (audit log — every Replay and Discard from this page must produce
an audit entry).
