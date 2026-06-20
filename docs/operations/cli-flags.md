# CLI Flags & `--set` Surface

The `fhir-subs` binary loads its configuration from a YAML file
(`--config`, default `/etc/fhir-subs/config.yaml`). For incident-time
overrides without a YAML edit, every operator-tunable knob below is
also reachable via `--set dotted.key=value` on the command line. Repeat
`--set` to override more than one key.

This document is the authoritative list of supported `--set` keys —
keys not in this list are rejected at startup so a typo doesn't
silently no-op.

## Top-level flags

| Flag | Purpose |
|---|---|
| `--config PATH` | Config file path. Default `/etc/fhir-subs/config.yaml`. |
| `--log-level LEVEL` | Override `deployment.log_level`. One of `debug`, `info`, `warn`, `error`. |
| `--check-config` | Validate the config + exit. No listener is opened, no DB is touched. |
| `--version` | Print the embedded build version + commit and exit. |
| `--help` | Usage. |
| `--set KEY=VALUE` | Override a config key. Repeat for multiple. See below. |

## Subcommands

The binary also accepts subcommands as the first positional argument:

| Subcommand | Purpose |
|---|---|
| `audit verify` | Walk the audit chain and report breaks. See `cmd/fhir-subs/audit_subcommand.go`. |
| `migrate up | status | down` | Run / inspect / reverse storage migrations. See `cmd/fhir-subs/migrate_subcommand.go`. |

## `--set` key matrix

Every supported key, grouped by config block. Type column tells you
the expected RHS shape.

### `deployment.*`

| Key | Type | Notes |
|---|---|---|
| `deployment.facility_id` | string | |
| `deployment.environment` | string | |
| `deployment.log_level` | string | Same as `--log-level`. |
| `deployment.log_format` | string | `json` or `text`. |

### `adapter.*`

| Key | Type | Notes |
|---|---|---|
| `adapter.id` | string | |
| `adapter.version_pin` | string | semver constraint. |

### `server.http.*`

| Key | Type | Notes |
|---|---|---|
| `server.http.bind` | host:port | |
| `server.http.insecure` | bool | When `false`, `tls.cert_file` + `tls.key_file` are required. |
| `server.http.tls.cert_file` | path | |
| `server.http.tls.key_file` | path | |
| `server.http.tls.min_version` | `1.2` or `1.3` | |
| `server.http.read_header_timeout` | duration | e.g. `5s`. |
| `server.http.read_timeout` | duration | |
| `server.http.write_timeout` | duration | |
| `server.http.idle_timeout` | duration | |
| `server.http.max_header_bytes` | int | bytes. |

### `lifecycle.*`

| Key | Type | Notes |
|---|---|---|
| `lifecycle.shutdown_grace_period` | duration | e.g. `30s`. |

### `database.*`

| Key | Type | Notes |
|---|---|---|
| `database.url` | DSN | `${env:VAR}` / `${file:/path}` interpolation runs at config load, not at `--set` time. |

### `auth.*`

| Key | Type | Notes |
|---|---|---|
| `auth.audience` | string | |
| `auth.token_url` | URL | |
| `auth.issued_issuer` | string | |
| `auth.issued_secret` | string | |
| `auth.allow_insecure_jwks` | bool | NEVER true in production. |
| `auth.access_token_ttl` | duration | OP #167 — was previously YAML-only. |
| `auth.jwks_cache_ttl` | duration | OP #167. |
| `auth.clock_skew` | duration | OP #167 — incident-time clock-skew rollout no longer requires a YAML edit + restart. |
| `auth.jwks_allowed_hosts` | YAML/JSON list | OP #167. e.g. `--set 'auth.jwks_allowed_hosts=["idp.example","backup.example"]'`. |
| `auth.trusted_issuers` | YAML/JSON list of structs | OP #167. e.g. `--set 'auth.trusted_issuers=[{"issuer":"https://idp","audience":"sub","jwks_url":"https://idp/jwks"}]'`. |
| `auth.subscription_create_rate_limit.burst` | int | Bucket capacity. `<=0` disables. |
| `auth.subscription_create_rate_limit.refill_per_second` | float | |
| `auth.subscription_create_rate_limit.max_keys` | int | |
| `auth.ws_binding_token_rate_limit.burst` | int | |
| `auth.ws_binding_token_rate_limit.refill_per_second` | float | |
| `auth.ws_binding_token_rate_limit.max_keys` | int | |

`auth.allow_dev_bypass`, `auth.dev_bypass_client_ids`, and
`auth.allow_subscriber_hosts` are intentionally NOT settable via
`--set`. They are dev / e2e knobs whose only correct production value
is the YAML default; an incident is not a reason to flip them.

### `topics.*`

| Key | Type | Notes |
|---|---|---|
| `topics.catalog_dir` | path | |

### `codec.*`

| Key | Type | Notes |
|---|---|---|
| `codec.active_key_version` | int | `codec.keys[]` itself is YAML-only; rotate via YAML + redeploy. |

### `admin.*`

| Key | Type | Notes |
|---|---|---|
| `admin.token` | string | Empty disables the admin surface. |
| `admin.path_prefix` | string | Default `/admin`. |
| `admin.rate_limit.burst` | int | |
| `admin.rate_limit.refill_per_second` | float | |
| `admin.rate_limit.max_keys` | int | |

### `channels.rest_hook.*`

| Key | Type | Notes |
|---|---|---|
| `channels.rest_hook.user_agent` | string | |
| `channels.rest_hook.request_timeout` | duration | |
| `channels.rest_hook.max_retry_after` | duration | OP #190. |
| `channels.rest_hook.min_retry_after` | duration | |

### `channels.websocket.*`

| Key | Type | Notes |
|---|---|---|
| `channels.websocket.idle_timeout` | duration | |
| `channels.websocket.ping_interval` | duration | |
| `channels.websocket.bind_timeout` | duration | |
| `channels.websocket.ping_write_timeout` | duration | |
| `channels.websocket.upgrade_read_header_timeout` | duration | |
| `channels.websocket.max_frame_bytes` | int | |
| `channels.websocket.max_sessions` | int | |
| `channels.websocket.max_sessions_per_client` | int | |
| `channels.websocket.max_replay_events` | int | |

### `channels.email.*`

| Key | Type | Notes |
|---|---|---|
| `channels.email.from` | RFC 5322 address | |
| `channels.email.subject_template` | string | |
| `channels.email.smtp_host` | string | |
| `channels.email.smtp_port` | int | |
| `channels.email.starttls` | string | `disabled`/`opportunistic`/`required`. |
| `channels.email.auth_mechanism` | string | |
| `channels.email.auth_username` | string | |
| `channels.email.auth_password` | string | |
| `channels.email.auth_identity` | string | |
| `channels.email.allow_cleartext_auth` | bool | |
| `channels.email.attachment_threshold_bytes` | int | |
| `channels.email.request_timeout` | duration | |
| `channels.email.local_name` | string | |
| `channels.email.user_agent` | string | |
| `channels.email.tls_min_version` | uint16 | TLS protocol constant. |

### `channels.message.*`

| Key | Type | Notes |
|---|---|---|
| `channels.message.user_agent` | string | |
| `channels.message.request_timeout` | duration | |
| `channels.message.server_endpoint` | URL | |
| `channels.message.max_idle_conns_per_host` | int | |
| `channels.message.max_conns_per_host` | int | |
| `channels.message.tls_min_version` | uint16 | |

## What's intentionally YAML-only

Some keys MUST stay YAML-only — they are too structured for a flat
dotted-key surface, or their wrong value is so dangerous that an
incident-time override is the wrong tool:

- `mllp.listeners[]` — list-of-structs with per-listener TLS. Edit YAML.
- `pipeline.*.claim_batch_size`, `pipeline.*.idle_poll_interval` — tune
  via YAML, not over a console.
- `storage.retention.*`, `storage.partitioning.*` — retention windows are
  audit-relevant; YAML keeps a clear change-log paper trail.
- `tracing.*` — operator recipes in
  [otel-exporter-recipes.md](otel-exporter-recipes.md) document a
  YAML-first flow that pairs with the env var convention.
- `codec.keys[]` — secret material, read via `${file:/path}`
  interpolation only.

## Error behavior

A malformed `--set` RHS produces an error of the form
`--set <key>=<redacted>: invalid value` — the value is intentionally
redacted because it may carry a secret (e.g. an SMTP password).
Operators get the key + the parse failure, not the value, in stderr.

## Where this lives in code

- Allowlist: `cmd/fhir-subs/config.go::applySets`.
- Tests: `cmd/fhir-subs/config_test.go::TestApplySets_*`.
- The validate path runs after `applySets`, so an override that pushes
  the config invalid (e.g. flipping `server.http.insecure=false`
  without supplying TLS material) still surfaces the same loud error
  the file-only path would.
