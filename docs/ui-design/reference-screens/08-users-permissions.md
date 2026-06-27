# 08 — Users & Permissions

> Mirth Connect "Users" tab in the left nav. User admin and per-user permission
> grants.

## What this screen shows in Mirth

The Users view is a two-pane layout:

- **Left**: a table of users with columns Username, First Name, Last Name,
  Email, Last Login. Above the table: New User / Edit User / Delete User
  / Refresh.
- **Right (or modal when editing)**: per-user details and a **permissions
  matrix**. Mirth uses a fine-grained permission model where each user can
  be granted/denied each of ~30 permission types:
  - View/Modify Server Settings
  - View/Modify Users
  - View/Modify Code Templates
  - View/Modify Channels (per-channel ACLs)
  - Deploy Channels
  - View Messages (per-channel)
  - Reprocess Messages (per-channel)
  - Remove Messages
  - View Server Logs
  - View Event Log
  - … and many more

Permissions can be assigned directly to a user or via a **Role** that groups
permissions. A user can have multiple roles.

There is also an **Event Log** tab adjacent to Users that shows server-side
audit events — a flat list of `user X did action Y at time Z` entries,
filterable by user, time range, and event class (login, channel-deploy,
user-edit, settings-change, etc.).

## What we're adapting

**For Users**: we do NOT have a Users page in v1. Our auth is OIDC, so users
come from Keycloak. There is no per-user grant UI. (If we did add a Users
page later, it would be read-only and just show "who has logged in.")

**For Permissions**: not applicable. Authz comes from OIDC group claims; the
mapping is configured in Helm values and shown on the Settings page (#406).

**For the Event Log**: this is the inspiration for our **Audit Log** (#407).
Take:

- A time-ordered list of "user X did action Y at time Z" entries
- Filterable by: user (OIDC subject or email), action type, time range,
  target resource (interface, subscription, DLQ entry)
- Each entry expandable to show the request payload that caused the action
  (e.g. the body of the Replay request)
- Action types we audit: Login, Activate Interface, Deactivate Interface,
  Replay DLQ Entry, Discard DLQ Entry, Reload Matchbox, Change Subscription
  State, View Sensitive Message (PHI access logging)
- Exportable to CSV for compliance review

## What we're explicitly NOT copying

- The per-user permission matrix. OIDC roles, not per-user grants. This is
  the single biggest divergence: Mirth ships with its own user database;
  we delegate to Keycloak.
- The "Users" tab itself in v1. Skip entirely; mention in Settings that
  identity is managed externally.
- The Event Log as a Swing-style flat table with grey beveled rows. Use a
  modern time-series log viewer (think Datadog or Grafana Loki Explore) —
  virtualised scroll, deep-link-friendly URLs, copy-as-cURL on each row.
- Mirth's lack of "view sensitive resource" auditing. We audit when an
  operator views message contents (PHI access) — Mirth doesn't.

## Which ticket implements our equivalent

**#407 (UI: audit log of operator actions).** The auditing requirements are
informed by HIPAA + the FHIR subscriptions service's PHI-handling profile,
so the audit log is more complete than Mirth's Event Log. There is no
direct equivalent of the Users / Permissions screens in v1 — identity lives
in Keycloak, not in our service.
