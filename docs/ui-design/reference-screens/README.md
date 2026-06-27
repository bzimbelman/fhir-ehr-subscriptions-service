# Mirth Connect Reference Screens

## What this directory is

A set of visual / textual references describing the *information architecture*
of the NextGen Connect (formerly Mirth Connect) Administrator UI. Each file
documents one screen of the legacy Mirth tool that the new fhir-subscriptions
operator UI (Epic #398, tickets #399 - #407) should mimic in **layout and data
shown**, **not** in styling.

Mirth Connect is a long-lived (since ~2006) HL7 interface engine. Its admin
client is a Java Swing desktop app — visually dated and dense, but the
*information architecture* (what data is shown on which screen, what filters
exist, what drill-down paths exist between screens) is well-considered and
maps almost 1:1 onto what an operator running an HL7-to-FHIR subscription
service needs.

## The rule: information architecture YES, styling NO

The project owner's explicit guidance during Epic #398 scoping was:

> "Mirth's UI is conceptually ok, ugly but functional. We're aiming at the same
> shape of information, not the same styling."

That means: when an implementing story (#400 dashboard, #401 per-interface,
#402 message viewer, etc.) reaches for a reference, they should look here for
**which data lives on which screen and how the screens link to each other**.
They should NOT copy:

- the Java Swing aesthetic (grey gradients, beveled panels, system fonts)
- the dense, no-whitespace table styling
- icon style (3D-shaded coloured beads/balls for status)
- the orange / blue color palette
- modal-heavy interaction model
- tabbed editor surface (we want a cleaner page-per-task layout)

## How to obtain Mirth screenshots yourself

This research effort started from the user's note that screenshots are useful
but not mandatory — Mirth Connect's Administrator is a Java Swing desktop
client and installing it just to take screenshots wasn't justified.

For one of the screens (the dashboard, `01-dashboard.png`) a public image was
recovered from the `nextgenhealthcare/connect` GitHub repository
(https://i.imgur.com/tnoAENw.png) showing the canonical dashboard layout. The
remaining seven screens are described in detail in Markdown — the descriptions
were authored from extensive public documentation and historical training
materials so that an implementing agent can build a faithful equivalent
without ever seeing the original.

If you want to see a screen first-hand, the easiest paths are:

- Watch any "Intro to Mirth Connect" YouTube tutorial (search "Mirth Connect
  Administrator tutorial" — many free walkthroughs exist).
- Download a community fork like the `mirth-connect` Docker images on Docker
  Hub and run it locally for ~15 minutes. **You do not need to do this to
  implement any of #399 - #407.**

## Files in this directory

| File                        | Format | Source         | Screen                                |
| --------------------------- | ------ | -------------- | ------------------------------------- |
| `01-dashboard.png`          | image  | Path A (imgur) | Channel grid + status indicators      |
| `02-channel-list.md`        | prose  | Path C         | Channel inventory with deploy/edit    |
| `03-channel-detail.md`      | prose  | Path C         | Per-channel stats + recent messages   |
| `04-message-browser.md`     | prose  | Path C         | Message search across a channel       |
| `05-message-detail.md`      | prose  | Path C         | One message, raw + parsed views       |
| `06-errored-messages.md`    | prose  | Path C         | Errors / dead-letter equivalent       |
| `07-server-settings.md`     | prose  | Path C         | Global config view                    |
| `08-users-permissions.md`   | prose  | Path C         | User admin (informs our audit shape)  |

## Per-ticket pointer table

Each implementation ticket should consult the reference screen(s) listed
below. The "We're adapting" column captures what to take; the "We're dropping"
column captures what to leave behind.

| Ticket | Subject                                              | Primary ref                          | Secondary ref(s)                      | We're adapting                                                                                          | We're dropping                                                                  |
| ------ | ---------------------------------------------------- | ------------------------------------ | ------------------------------------- | ------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------- |
| #399   | Scaffold Next.js + OIDC login                        | n/a (infra only)                     | `07-server-settings.md` for nav shape | Top-level navigation skeleton: Dashboard / Channels (Interfaces) / Messages / Settings                  | Swing menus, login dialogs                                                      |
| #400   | Dashboard — health + counters                        | `01-dashboard.png`                   | `02-channel-list.md`                  | Channel grid with per-row Received / Filtered / Queued / Sent / Errored counters; live status indicator | Coloured beads, dense table, "Lifetime Statistics" radio toggle (use a tab)     |
| #401   | Per-interface state + stats                          | `03-channel-detail.md`               | `01-dashboard.png`                    | Per-source-system tab with deploy state, counters, queue depth, last message time                       | Tabbed inline editor; channel "Revert / Redeploy" buttons                        |
| #402   | Message viewer — search + drill-down + raw + parsed  | `04-message-browser.md`              | `05-message-detail.md`                | Search-then-list workflow, filters down the left, table in the centre, detail pane right-or-modal       | The Swing tabbed message detail with five tabs (Raw / Transformed / etc.)       |
| #403   | DLQ viewer with replay + delete                      | `06-errored-messages.md`             | `04-message-browser.md`               | Errored-message list with replay action; group by error class; show last-attempt timestamp + reason     | Mirth's confusing "Reprocess vs Send" duality                                   |
| #404   | Subscriptions viewer with delivery history           | `04-message-browser.md`              | `06-errored-messages.md`              | Per-subscription delivery log with status per attempt; expandable to payload                            | Channel-centric framing (we're subscription-centric)                            |
| #405   | Matchbox view — StructureMaps + reload               | `07-server-settings.md`              | `03-channel-detail.md`                | Read-only list of loaded artefacts with version + last-loaded timestamp, plus a Reload action            | Mirth's "Code Templates" library UI (different concept, but vibe of read-only)  |
| #406   | Settings view — effective config + version info      | `07-server-settings.md`              | n/a                                   | Single read-only page with grouped sections; copy-friendly values; build / version footer               | Mirth's massive Settings dialog with 5+ tabs                                    |
| #407   | Audit log of operator actions                        | `08-users-permissions.md`            | `04-message-browser.md`               | Time-ordered list of "who did what when", filterable by user and action type                             | Mirth's per-user permissions matrix (we have OIDC roles, not per-user grants)   |

## Naming convention in this repo

The new operator UI uses **"Interface"** where Mirth uses **"Channel"**. Same
concept: a deployed source-system → destination-subscriber pipeline. When you
read these reference docs, mentally substitute the terms:

- Mirth "Channel" → our "Interface"
- Mirth "Source Connector" → our "Inbound endpoint / receiver"
- Mirth "Destination" → our "Subscription"
- Mirth "Deploy" → our "Activate" (deployment is a separate ops concern in our
  world; the UI doesn't deploy code)
- Mirth "Queued" → our "Pending delivery"
- Mirth "Errored" → our "DLQ entry"

## Image attribution

`01-dashboard.png` was retrieved from a public link referenced in the
`nextgenhealthcare/connect` GitHub repository README
(https://i.imgur.com/tnoAENw.png). NextGen Connect is the trademark of NextGen
Healthcare. This image is reproduced here under fair-use for internal
engineering reference only; do not republish externally.

## Dashboard caption (image-only file `01-dashboard.png`)

**What this screen shows in Mirth.** The Dashboard is the operator's home
view. The top half is a table of every deployed Channel with columns: Status
(coloured bead), Name, Rev Δ (revision-out-of-date marker), Last Deployed
timestamp, then five message-counter columns: Received, Filtered, Queued,
Sent, Errored. The far right column shows source-connection state ("Idle",
"Connected", "Polling"). The bottom half is a tabbed log panel with Server
Log / Connection Log / Global Maps, streaming server-side log events.

**What we're adapting.** The 6-column "what flowed through this channel since
deploy" counter strip is the single highest-signal datum an operator wants —
we keep it 1:1 for each Interface row. The split between "list of pipelines"
on top and "global log / system events" on bottom is also right. The fact
that Status, Name, and counters all live in one row means an operator can
scan the dashboard and instantly see "interface X is healthy and pushing N
messages/min; interface Y has stopped receiving."

**What we're explicitly NOT copying.** The Java Swing chrome (grey beveled
table, fake-3D scrollbars, system menu bar), the coloured beads for status
(use semantic Tailwind colours and an inline label like "Healthy" / "Stale"),
and the "Current Statistics / Lifetime Statistics" radio (use a tab instead).
Our table should breathe — wider rows, monospaced numbers, sortable headers.

**Implementing ticket.** #400 (UI: dashboard — system health + top-level
counters). Also referenced by #401 for the channel-list layout pattern.
