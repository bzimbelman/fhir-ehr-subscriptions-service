# Hot config reload (SIGHUP and secret-file rotation)

The `fhir-subs` binary observes two reload triggers without restarting:

| Trigger     | Source                                  | Story   |
| ----------- | --------------------------------------- | ------- |
| `sighup`    | `kill -HUP <pid>` (operator-initiated)  | #151    |
| `file_mtime`| Any `${file:...}` placeholder rotated   | #152    |

Both follow the same path:

1. `loadConfig` re-reads the operator-supplied YAML from `--config`.
2. The `${env:...}` and `${file:...}` placeholders re-interpolate.
3. The result is `Validate()`-d.
4. The post-merge view is compared against the in-memory snapshot.
   Any change to a field marked **immutable** (table below) causes a
   `config reload rejected` log line; the prior values stay live.
5. On accept, the **hot-reloadable** subset is applied to live
   components and a `config reload applied` line is logged with the
   trigger label.

## Hot-reloadable fields

These take effect on the next reload:

| Field                         | Live effect                                              |
| ----------------------------- | -------------------------------------------------------- |
| `deployment.log_level`        | swapped via `slog.LevelVar` — every subsequent record    |
| `deployment.log_format`       | re-read but does not change the running handler today    |
| `topics.catalog_dir` contents | matcher `AtomicCatalogProvider` swaps in the new catalog |
| `codec.keys` (key rotation)   | re-read; codec rotation honored at the next operation    |
| `auth.subscription_create_rate_limit` | re-read; per-client rate limiters honor the new burst/refill at the next bucket refresh |
| `auth.ws_binding_token_rate_limit`    | as above                                                 |

## Immutable fields

A reload is **rejected with a WARN** if any of these change. Operators
rotate them by rolling pods, not by signaling:

- `database.url`
- `mllp.listeners` (any addition, removal, or rebind)
- `server.http.bind`
- `server.http.probe_bind`
- `server.http.insecure`
- `server.http.tls.*`
- `lifecycle.shutdown_grace_period`

A rejected reload leaves every prior value live. The next reload
attempt after the operator reverts the offending field will succeed.

## Secret-file watcher

The binary scans the on-disk config bytes for every `${file:/abs/path}`
placeholder at boot and after every accepted reload. Each path is
`stat`-ed every `deployment.secret_file_poll_interval` (default `60s`).
When any path's mtime moves, the binary fires a `file_mtime` reload.

This is the rotation path Vault Agent and cert-manager use: rotate the
on-disk file, no signaling rights into the process.

A rotated file with structurally valid contents is hot-applied; a
rotated file that fails validation is rejected with a WARN — the prior
in-memory values remain live so the data plane keeps running on the
last good config.

## Observability

Every reload — accepted or rejected — emits a structured log line:

```text
{"msg":"config reload applied","trigger":"sighup","applied_paths":["deployment.log_level"]}
{"msg":"config reload rejected: immutable fields changed","trigger":"sighup","rejected_paths":["database.url"]}
{"msg":"config reload applied","trigger":"file_mtime","applied_paths":["deployment.log_level"]}
```

The `trigger` label is the source of truth for the
`fhir_subs_config_reload_total{trigger}` metric. <!-- docs-lint:ignore-metric=fhir_subs_config_reload_total -->

This metric is not yet registered in the production binary's metrics
path; the lifecycle module emits the structured log line and the
metric is wired by a follow-on story.

## What is NOT covered

- File-watcher-based reload of the config file itself (only the
  operator-supplied placeholder paths are watched today). A
  fsnotify/inotify-driven watch on the config file is tracked
  separately.
- Hot rotation of TLS cert/key paths into the running listener: the
  paths are immutable in this model. A rotation that needs the
  listener to pick up new cert material requires a pod roll.
