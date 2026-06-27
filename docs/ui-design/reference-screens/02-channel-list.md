# 02 — Channel List

> Mirth Connect "Channels" view (left-nav: **Channels**). The inventory of all
> channels (deployed or not).

## What this screen shows in Mirth

The Channels view is reached from the left navigation rail and replaces the
Dashboard in the main pane. It shows a **table of all channels that exist on
the server** — both deployed (live) and undeployed (defined but not running).

Columns from left to right:

- Status — coloured bead (green = started, yellow = paused, red = stopped,
  grey = undeployed).
- Name — channel name (clickable to open the channel editor).
- Id — UUID-ish opaque identifier (small, grey).
- Last Modified — timestamp of last edit.
- Last Deployed — timestamp of last deploy.
- Rev Δ — small icon (yellow "!") if the on-disk revision is newer than the
  deployed revision; blank if in sync.

Above the table is a toolbar with action buttons that operate on the
currently-selected row(s):

- **New Channel** — opens the channel editor for a fresh channel
- **Import Channel** — file picker for `.xml` channel exports
- **Export Channel** — saves selected channel as `.xml`
- **Clone**, **Edit**, **Delete**
- **Deploy / Undeploy / Redeploy** — pushes the on-disk definition to the
  running server
- **Refresh** — re-pulls the channel list from the server
- **Enable / Disable** — toggles whether a deployed channel actively processes
  messages

Channels can be organized into folder-like **Channel Groups**, shown as
expandable rows above their member channels.

## What we're adapting in our equivalent UI

For the new operator UI, this maps onto an **Interfaces** index page (linked
from the global nav). What we take:

- A single table-style inventory of every Interface defined in the cluster
- Per-row state badge (Active / Inactive / Stopped / Misconfigured)
- Last modified and last deployed columns — operators care about freshness
- The Rev Δ idea — if a Git-tracked definition is ahead of what's running,
  surface it as a "Pending update" badge so an operator can see drift
- A small set of bulk actions on selected rows (Activate, Deactivate, view
  config diff)
- Group rows above their members, so e.g. all "Latitude" interfaces collapse
  under one group header

Clicking an interface row navigates to the per-interface page
(`03-channel-detail.md` equivalent), **not** to an inline editor — we want
view-then-act, not Swing's tabbed-editor model.

## What we're explicitly NOT copying

- Mirth's "Channel Editor" is a big tabbed Swing dialog covering Summary,
  Source, Destinations, Scripts, Filter, Transformer, etc. We do not edit
  channel definitions in our UI at all in v1 — those live in code/config
  managed via Git + CI. Our screen is **read + activate**, not edit.
- The action toolbar of 15+ buttons. Mirth puts every imaginable verb on the
  toolbar; we want a small primary-action set with overflow.
- Coloured beads. Status is a textual badge with a semantic colour.
- Channel "Import / Export" as XML files — not relevant to our model.
- Per-channel "Clone" — we don't clone interfaces from inside the UI.

## Which ticket implements our equivalent

**#401 (UI: per-interface state and stats)** owns the per-interface drilldown,
but its parent index page (the Interfaces table) is implicitly part of #401's
scope as the landing pad. Reference this doc alongside `01-dashboard.png`
when laying out either screen.
