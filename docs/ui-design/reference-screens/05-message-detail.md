# 05 — Message Detail (raw + parsed view)

> Mirth Connect Message Browser → click a row. The bottom-right detail pane.

## What this screen shows in Mirth

When you select a row in the message list, the bottom-right detail panel
shows a **tabbed view of one message at each stage of the pipeline**:

| Tab            | Content                                                                 |
| -------------- | ----------------------------------------------------------------------- |
| Raw            | The exact bytes received from the source (HL7 v2 pipe-bar text usually) |
| Processed Raw  | After any pre-parse normalization                                       |
| Transformed    | The intermediate object form (Mirth's E4X/JS internal representation)   |
| Encoded        | After the destination's encoder turns it back to wire format            |
| Sent           | What was actually pushed on the wire to the destination                 |
| Response       | What the destination replied (e.g. HL7 ACK, HTTP status + body)         |
| Mappings       | Key/value pairs the channel script set on this message (`channelMap`)   |
| Errors         | Any error messages, with stack traces                                   |

Above the tab strip is a small header band with: Message ID, received-at
timestamp, status, connector name, and a "Reprocess this message" button.

Within each tab, Mirth shows the content in a monospaced **read-only text
area** with line numbers and minimal syntax recognition (Raw HL7 colorizes
segment names; Transformed is shown as Java/JS object text).

There's a copy-to-clipboard icon per tab.

## What we're adapting

A **Message Detail view** at `/messages/[id]` (or as a slide-over panel on
list-view). It should have:

- Header: message ID, source interface, received timestamp, current status
  badge, last-updated timestamp
- A **small tab set** (≤4 tabs):
  1. **Raw** — exactly what came in (HL7 v2 or v3, or whatever wire format)
  2. **Parsed** — our intermediate FHIR representation (the JSON Bundle or
     Resource we built from the source message)
  3. **Deliveries** — one row per subscription that was supposed to receive
     this; each row has its own status + last-attempt timestamp, expandable
     to show response body
  4. **Errors** — if any. Empty if status is "Delivered to all".
- A small action set: Replay (to all failed subscriptions), Copy ID, View in
  interface context (links back to `03-channel-detail.md`).

Monospaced syntax-highlighted text view for Raw and Parsed (JSON gets pretty
syntax highlighting; HL7 v2 gets segment-name highlighting).

## What we're explicitly NOT copying

- The 8-tab detail panel. Our pipeline isn't 8 stages — it's roughly
  Raw → Parsed → Delivered (per subscriber). Three tabs plus an optional
  Errors tab is enough.
- The "Processed Raw / Transformed / Encoded" trio is Mirth-pipeline-specific
  and would confuse our operators.
- Mappings tab — we don't have a `channelMap` equivalent in our model; if
  there's metadata to show, fold it into the Parsed view as a small key/value
  header.
- The Swing text-area aesthetic (small font, no margins, beveled border).
  Use a clean code-viewer component (Monaco-readonly, CodeMirror-readonly,
  or even a simple `<pre>` with good typography).
- Inline message "Reprocess" button — replay actions are surfaced in the DLQ
  view (#403) and per-subscription view (#404). One source of truth for the
  replay action.

## Which ticket implements our equivalent

**#402 (UI: message viewer — search, drill-down, raw + parsed view).** The
"raw + parsed" framing in the ticket title maps exactly to the two primary
tabs above. Secondary reader: #404 for the Deliveries tab — its row-per-
attempt shape should be consistent with how the subscription history page
shows attempts.
