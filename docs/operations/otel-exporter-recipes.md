# OpenTelemetry Trace Exporter — Deployment Recipes

This guide covers deploying the OTLP HTTP trace exporter against the major
managed-tracing back-ends. The transport surface lives in
`internal/infra/observability/tracing/tracing.go`; operators configure it via
the `tracing` block of `fhir-subs.yaml` (or the matching env vars exposed by
the config loader).

## Configuration surface

| Option | Type | Default | Notes |
|---|---|---|---|
| `tracing.otlp_endpoint` | string | empty (disables tracing) | Full HTTPS URL of the OTLP collector |
| `tracing.sample_rate` | float | 0.1 | Head sampling rate (0.0–1.0) |
| `tracing.exporter_timeout` | duration | 5s | Per-call deadline for exporter sends |
| `tracing.insecure` | bool | false | Allow plaintext to non-loopback (refuses by default) |
| `tracing.tls.cert_file` | path | — | Client cert for mTLS |
| `tracing.tls.key_file` | path | — | Client key for mTLS |
| `tracing.tls.ca_file` | path | — | Trusted root CA bundle |
| `tracing.headers.<name>` | string | — | Static header forwarded with every OTLP request |

Plaintext to a non-loopback endpoint is refused unless `insecure: true` is
set explicitly.

## Datadog (Agent OTLP intake)

Datadog accepts OTLP via the local `datadog-agent` container with OTLP intake
enabled (`DD_OTLP_CONFIG_RECEIVER_PROTOCOLS_HTTP_ENDPOINT=0.0.0.0:4318`).
With the agent on the same node, plaintext to `127.0.0.1:4318` is acceptable.

```yaml
tracing:
  otlp_endpoint: "http://127.0.0.1:4318/v1/traces"
  sample_rate: 0.1
  insecure: true
```

For agentless intake (Datadog OTLP HTTP at
`https://trace.agent.<site>.datadoghq.com/api/v0.2/traces`) headers carry the
API key:

```yaml
tracing:
  otlp_endpoint: "https://trace.agent.datadoghq.com/api/v0.2/traces"
  sample_rate: 0.1
  headers:
    DD-API-KEY: "${DD_API_KEY}"
```

## Honeycomb

Honeycomb accepts OTLP HTTP at `https://api.honeycomb.io:443/v1/traces`.
Authentication is the `x-honeycomb-team` header; an explicit dataset header
selects the dataset for the spans.

```yaml
tracing:
  otlp_endpoint: "https://api.honeycomb.io:443/v1/traces"
  sample_rate: 0.05
  headers:
    x-honeycomb-team: "${HONEYCOMB_API_KEY}"
    x-honeycomb-dataset: "fhir-subs"
```

For deterministic-sampling deployments, drop `sample_rate` to 1.0 and apply
sampling rules at the Honeycomb side.

## Jaeger

Jaeger 1.35+ exposes OTLP HTTP on port 4318 of the collector. With a
collector reachable inside the cluster (e.g., service `jaeger-collector`
in namespace `observability`):

```yaml
tracing:
  otlp_endpoint: "http://jaeger-collector.observability.svc:4318/v1/traces"
  sample_rate: 1.0
  insecure: true
```

Internal cluster addresses are not loopback; the `insecure: true` toggle is
required when TLS is terminated at an upstream ingress and not at the
collector itself.

## Grafana Tempo

Tempo's OTLP HTTP receiver is on port 4318 by default. Tempo Cloud uses the
same endpoint at `https://tempo-prod-04-prod-us-east-0.grafana.net:443/v1/traces`
with basic auth carried in the `Authorization` header.

```yaml
tracing:
  otlp_endpoint: "https://tempo-prod-04-prod-us-east-0.grafana.net:443/v1/traces"
  sample_rate: 0.1
  headers:
    Authorization: "Basic ${TEMPO_BASIC_AUTH}"
```

For self-hosted Tempo behind an internal LB:

```yaml
tracing:
  otlp_endpoint: "http://tempo-distributor.observability.svc:4318/v1/traces"
  sample_rate: 0.1
  insecure: true
```

## Smoke test

Operators verify the exporter is wired by emitting a trace and observing it
in the back-end:

```bash
# Issue a request that produces a span
curl -i -H 'Accept: application/fhir+json' http://fhir-subs.example.com/metadata
# Within ~5s, the span "GET /metadata" should appear in the chosen back-end.
```

If no span appears:

1. Check `fhir_subs_observability_tracing_export_failures_total` on the
   `/metrics` endpoint. Non-zero means the exporter is configured but
   cannot reach the collector.
2. Check `fhir_subs_observability_tracing_spans_dropped_total` for queue
   pressure; the exporter drops spans when the queue is full and the
   collector is unreachable.
3. Run with `LOG_LEVEL=debug` and look for `tracing: OTLP exporter:` log
   lines that name the failure.

## Notes

- The exporter uses HTTP (`otlp/http`); gRPC is not currently wired in.
  Trace back-ends that only accept gRPC (e.g., AWS X-Ray native) need a
  collector-side translator (e.g., the OpenTelemetry Collector run as a
  sidecar).
- Sampling is head-based. Tail sampling, if needed, runs in a collector
  upstream of the back-end, not in this service.
- The PHI-redaction layer at `tracing.SafeAttribute` (S-14.8) strips PHI
  from span attributes before export. Custom span tags added in
  application code must call `SafeAttribute` before `span.SetAttributes`.
