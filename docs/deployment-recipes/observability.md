# Observability — bundled Prometheus + Grafana + Jaeger

> Scope: dev / small deployments via Docker Compose. Operators running on Kubernetes should use the bundled ServiceMonitor in the Helm chart against a real Prometheus Operator (and a real long-term store), not this profile. See [Scope and production](#scope-and-production) below.

Ticket [#395](https://op.bzonfhir.com/openproject/projects/subscription-service/work_packages/395) packages Prometheus, Grafana, and Jaeger as an OPTIONAL Docker Compose profile so an operator can stand the stack up, point a browser at port 13000, and see pipeline health with zero extra configuration.

## What's in the profile

All three services live under the `observability` Compose profile, so they only start when you pass `--profile observability` to `docker compose`:

| Service     | Port  | Purpose                                                                                                    |
|-------------|-------|------------------------------------------------------------------------------------------------------------|
| Prometheus  | 9090  | Scrapes `/actuator/prometheus` on `interface-engine:8090` and `hapi:8080` every 15s; 14-day retention.     |
| Grafana     | 13000 | UI for dashboards; admin/admin by default; reads metrics from the bundled Prometheus.                       |
| Jaeger      | 16686 | OTLP collector (`:4317` gRPC, `:4318` HTTP) + trace UI. Added by ticket [#394](https://op.bzonfhir.com/openproject/projects/subscription-service/work_packages/394). |

Without `--profile observability`, all three are silently ignored and `docker compose up -d` brings up only the base stack (HAPI + Postgres + Matchbox + interface-engine) — same as before this ticket landed.

## Bring it up

```bash
cd deploy/docker
cp .env.example .env                    # first-time setup
# Optional: rotate the Grafana admin password BEFORE you start it.
sed -i.bak "s/^GRAFANA_ADMIN_PASSWORD=.*/GRAFANA_ADMIN_PASSWORD=$(openssl rand -hex 16)/" .env && rm .env.bak

docker compose --profile observability up -d
```

Watch the services come ready:

```bash
docker compose --profile observability ps
```

You should see seven containers (the four base services plus `prometheus`, `grafana`, `jaeger`). Wait for each to report `(healthy)`; Grafana takes ~20s on first boot to provision the datasource and dashboards.

## Open the dashboards

- Grafana UI: <http://localhost:13000>
  - Default login: `admin` / `admin` (rotate before any non-laptop use — see [Rotate the Grafana password](#rotate-the-grafana-password)).
- Prometheus UI (raw query exploration): <http://localhost:9090>
- Jaeger UI (traces): <http://localhost:16686> (only useful if you've enabled OpenTelemetry — see the [Jaeger recipe](#enable-tracing-to-jaeger)).

Once logged in, the bundled dashboards live in the **subscription-service** folder.

## What ships out of the box

Three pre-built dashboards, auto-loaded by [`provisioning/dashboards/default.yaml`](../../deploy/docker/grafana/provisioning/dashboards/default.yaml) from `/var/lib/grafana/dashboards`:

| Dashboard               | uid                                       | Purpose                                                                                                              |
|-------------------------|-------------------------------------------|----------------------------------------------------------------------------------------------------------------------|
| Pipeline Health         | `subscription-service-pipeline-health`    | Inbound rate, DLQ size, transform / HAPI POST p95-p99, end-to-end latency, DLQ transitions by reason. The first thing to open during an incident. |
| Subscription Delivery   | `subscription-service-delivery`           | Active Subscriptions, delivery success ratio, delivery latency p95 by outcome, top failing channel types.            |
| System (JVM)            | `subscription-service-system`             | Heap, HTTP request rate by URI, GC pause time, HikariCP pool utilization, JVM thread count for both services.        |

All three are powered by the v1.0 metric catalog ([`docs/observability/metric-catalog.md`](../observability/metric-catalog.md)). The first two dashboards rely on the REQUIRED metrics — they'll keep working through any MINOR catalog bump. The System dashboard depends on default Spring Boot / Micrometer metrics, which are OPTIONAL tier; queries there may need to shift if Micrometer changes shape.

## Rotate the Grafana password

The compose stack defaults `GRAFANA_ADMIN_PASSWORD` to `admin`. That's only acceptable for a fully-isolated laptop. Anywhere else:

1. Pick a value: `openssl rand -hex 16` or `pwgen 32 1`.
2. Set `GRAFANA_ADMIN_PASSWORD=<value>` in `.env` BEFORE the first `up`. Grafana sets the admin password from this env var on first run only; rotating after the fact requires either `grafana-cli admin reset-admin-password` inside the container or wiping the Grafana volume.

If you've already started the stack and want to rotate:

```bash
docker compose exec grafana grafana-cli admin reset-admin-password "$(openssl rand -hex 16)"
```

The new password prints to stdout; copy it before you close the terminal.

## Enable tracing to Jaeger

The OpenTelemetry SDK ships disabled by default. To turn it on with the bundled Jaeger collector:

1. Add to `.env`:
   ```
   OTEL_SDK_DISABLED=false
   OTEL_EXPORTER_OTLP_ENDPOINT=http://jaeger:4317
   ```
2. `docker compose --profile observability up -d` re-applies the env to interface-engine + HAPI.
3. Send some traffic, then open <http://localhost:16686> to see traces from both services.

Full reference: ticket [#394](https://op.bzonfhir.com/openproject/projects/subscription-service/work_packages/394) and the matching env block in `deploy/docker/.env.example`.

## Send a test message and watch the dashboard tick

With the stack up:

```bash
# Send a sample ADT^A04 over MLLP. The interface-engine ACKs synchronously
# and then the worker drives the message through transform → HAPI POST.
nc -w 1 localhost 2575 < interface-engine/src/test/resources/hl7/adt-a04.hl7
```

(The exact fixture path may differ; any `.hl7` file in your test fixtures works.)

Within ~30s, refresh the **Pipeline Health** dashboard. The **Inbound messages (total)** stat should tick up by 1; the **Inbound rate by status** time-series should show a brief blip on `DELIVERED` (or `DEAD_LETTER` if the transform failed).

## Tear down

```bash
cd deploy/docker
docker compose --profile observability down
```

Add `-v` to also drop the Grafana / Prometheus storage volumes. Without `-v` Grafana keeps its provisioning database (admin password, dashboard versions) across restarts; with `-v` you start fresh next time.

## Scope and production

The bundled Prometheus + Grafana are a DEV / SMALL-DEPLOYMENT recipe. Specifically:

- **No long-term storage**. The single-node Prometheus retention is set to 14 days (`--storage.tsdb.retention.time=14d`). For real production, you need Thanos / Cortex / Mimir, a managed Prometheus, or one of Datadog / Honeycomb / Grafana Cloud / Chronosphere.
- **No HA**. One Prometheus + one Grafana, both bind-mounted off the host. Lose the host, lose your dashboards.
- **No alerting wired up**. The bundled Grafana has Alertmanager-style routing built in but we don't ship rules or notification channels. Alerts belong on your real monitoring stack.
- **Kubernetes operators**: ignore this profile. The Helm chart bundles a Prometheus Operator [`ServiceMonitor`](../../deploy/k8s/charts/subscription-service/templates/servicemonitor.yaml) (ticket [#418](https://op.bzonfhir.com/openproject/projects/subscription-service/work_packages/418)); point your in-cluster Prometheus at that instead of running this docker-compose stack alongside.

Treat this profile as "the absolute minimum that lets a new operator see how their pipeline is behaving before they invest in a real observability stack."

## Provisioning files

| File                                                                                                  | Purpose                                                                |
|-------------------------------------------------------------------------------------------------------|------------------------------------------------------------------------|
| [`deploy/docker/prometheus/prometheus.yml`](../../deploy/docker/prometheus/prometheus.yml)            | Prometheus scrape config (two jobs).                                   |
| [`deploy/docker/grafana/provisioning/datasources/prometheus.yaml`](../../deploy/docker/grafana/provisioning/datasources/prometheus.yaml) | Auto-loaded Prometheus datasource. |
| [`deploy/docker/grafana/provisioning/dashboards/default.yaml`](../../deploy/docker/grafana/provisioning/dashboards/default.yaml) | Dashboard provider; loads everything under `/var/lib/grafana/dashboards`. |
| `deploy/docker/grafana/dashboards/*.json`                                                             | The three dashboards listed above.                                     |

To add another dashboard: drop the JSON under `deploy/docker/grafana/dashboards/`. Grafana re-scans the directory every 30s (`updateIntervalSeconds: 30` in the provider config) — no restart needed.

## Validation

`scripts/observability/test-dashboards.sh` parses every `*.json` under `deploy/docker/grafana/dashboards/`, asserts the basic Grafana 11 schema (`title`, `uid`, `schemaVersion`, `.panels`, each panel's `id`/`type`/`gridPos`, no empty PromQL `.expr`), and parses the three provisioning YAML files. The repo's CI calls it on every push.

```bash
bash scripts/observability/test-dashboards.sh
```

## See also

- [`docs/observability/metric-catalog.md`](../observability/metric-catalog.md) — v1.0 contract for the metrics the dashboards query.
- [`docs/observability/log-schema.md`](../observability/log-schema.md) — sister contract for structured logs.
- [Ticket #389](https://op.bzonfhir.com/openproject/projects/subscription-service/work_packages/389) — Prometheus endpoint implementation.
- [Ticket #394](https://op.bzonfhir.com/openproject/projects/subscription-service/work_packages/394) — OpenTelemetry / Jaeger.
- [Ticket #418](https://op.bzonfhir.com/openproject/projects/subscription-service/work_packages/418) — Helm ServiceMonitor (the production analogue of this recipe).
