# 07 — Server Settings

> Mirth Connect "Settings" tab in the left nav. The global server-wide config
> page.

## What this screen shows in Mirth

The Settings view is a vertical tab strip down the left edge of the main
pane with these sections:

| Tab                  | Content                                                                                |
| -------------------- | -------------------------------------------------------------------------------------- |
| Server               | Hostname, server ID, time zone, queue buffer size, max log file size                   |
| Administrator        | Background colour, default refresh interval, channel-deploy prompt confirmation        |
| Tags                 | Define tags for organizing channels (key/value)                                        |
| Configuration Map    | Server-wide key/value pairs available to channel scripts (Mirth's "env vars")          |
| Database             | Connection pool size, query timeout, JDBC URL (read-only, configured at install)       |
| Email                | SMTP host, sender address, default subject (used by channel scripts to send alerts)    |
| Logging              | Log levels per package, log file rotation policy                                       |
| Resources            | JARs and external libraries loaded at startup                                          |
| Data Pruner          | Schedule for pruning historical message data (retention policy)                        |
| Alerts               | Define alert rules (e.g. "send email when channel X errors > 10 in 5min")              |

Each tab is a Swing form with text inputs, dropdowns, and a Save button at
the bottom. Saving applies the setting and (for some) prompts a server
restart warning. Most tabs include a Reset to Defaults button.

Above the tab strip there is no global search of settings; each tab is found
by scanning the strip.

## What we're adapting

Our **Settings view** at `/settings` is **read-only** in v1 — it's a place
to view what's currently configured, not change it. (Changes flow through
Git + CI in our model.) It should:

- Show **grouped sections** on one scrollable page rather than tabs (the
  total volume of settings is much smaller than Mirth's):
  1. **Service** — version, build SHA, deployed environment, uptime
  2. **Identity (OIDC)** — issuer URL, client ID (no secrets!), allowed
     groups/roles
  3. **Subscriptions** — default retry policy, default backoff, max in-flight
  4. **Retention** — message retention window, DLQ retention window
  5. **Observability** — Prometheus scrape endpoint, Grafana dashboard link,
     log level
  6. **Cluster** — Kubernetes namespace, pod count, current leader pod, last
     deploy timestamp
- Every value should be **copy-button** adjacent (engineers copy config into
  support tickets all the time)
- Each section has a small "Source of truth" badge with a link to the
  Git path of the controlling Helm value / ConfigMap, so an operator can
  trace from "what I see" to "where I'd edit it"

The Matchbox-related "loaded StructureMaps + version + reload" (#405) is its
own page, not folded into settings — but the *visual style* of #405 (a clean
read-only list of artefacts with version + last-loaded timestamp) is the
same as Settings sections.

## What we're explicitly NOT copying

- The 10-tab vertical strip. Use one scrollable page with sticky section
  headers (or a small TOC on the side).
- The "Configuration Map" key/value editor — we don't have channel-script
  globals; environment-specific values come from Helm values / sealed
  secrets.
- Email SMTP settings — alerts go through Alertmanager/PagerDuty, not via
  in-app SMTP.
- The Data Pruner / Alerts custom-rule tabs. Retention is configured in Helm
  values; alerts are configured in Alertmanager. Don't pretend the UI owns
  them.
- The "Save" button at the bottom of every section. Read-only page.

## Which ticket implements our equivalent

**#406 (UI: settings view — effective config + version info).** Secondary
readers: #399 (scaffold) for the global nav slot, #405 (Matchbox) for the
read-only-artefact-list visual pattern shared with Settings sections.
