# OpenTelemetry Trace Exporter — Deployment Recipes

This guide covers deploying the OTLP HTTP trace exporter against the major
managed-tracing back-ends. The transport surface lives in
`internal/infra/observability/tracing/tracing.go`; operators configure it via
the top-level `tracing:` block of the binary's YAML config (parsed by
`cmd/fhir-subs/config.go::TracingConfig`).

The production binary wires the configured `tracing:` block through
`cmd/fhir-subs/wiring_observability.go::buildObservabilityConfig` into
`observability.Start`, which constructs the OTel `TracerProvider` and
mounts the HTTP middleware (`handlers.TracingMiddleware`) on the FHIR
API listener.

## Configuration surface

These are the keys the binary actually parses. Anything else under
`tracing:` is silently absorbed by the loader's `Extra` catch-all and
will not affect runtime behavior.

| Option | Type | Default | Notes |
|---|---|---|---|
| `tracing.otlp_endpoint` | string | empty (disables tracing) | Full HTTP(S) URL of the OTLP collector |
| `tracing.sample_rate` | float | 0.1 (`tracing.DefaultSampleRate`) | Head sampling rate (0.0–1.0); validated by config loader |
| `tracing.exporter_timeout` | duration | 10s (`tracing.DefaultExporterTimeout`) | Per-call deadline for exporter sends and the `otlptrace.New` build |
| `tracing.insecure` | bool | false | Allow plaintext to non-loopback collectors. Default refuses non-loopback HTTP. |
| `tracing.tls.cert_file` | path | — | Client cert for mTLS (PEM) |
| `tracing.tls.key_file` | path | — | Client key for mTLS (PEM) |
| `tracing.tls.ca_file` | path | — | Trusted root CA bundle (PEM) |
| `tracing.headers.<name>` | string | — | Static header forwarded with every OTLP request |

Plaintext to a non-loopback endpoint is refused unless `insecure: true`
is set explicitly **or** `tracing.tls.*` is populated. The check lives
in `tracing.New` (see `isLoopbackEndpoint`); it accepts `localhost`,
`127.0.0.1`, and `[::1]` as loopback.

Empty `otlp_endpoint` disables tracing entirely; the module returns a
no-op tracer (`tracing.Module.Disabled() == true`) and the HTTP
middleware becomes a passthrough.

## Datadog (Agent OTLP intake)

Datadog accepts OTLP via a node-local `datadog-agent` container with
OTLP intake enabled
(`DD_OTLP_CONFIG_RECEIVER_PROTOCOLS_HTTP_ENDPOINT=0.0.0.0:4318`).
With the agent on the same node, plaintext to `127.0.0.1:4318` is
acceptable (loopback).

```yaml
tracing:
  otlp_endpoint: "http://127.0.0.1:4318/v1/traces"
  sample_rate: 0.1
```

For agentless intake (Datadog OTLP HTTP at
`https://trace.agent.<site>.datadoghq.com/api/v0.2/traces`), pass the
API key as a header:

```yaml
tracing:
  otlp_endpoint: "https://trace.agent.datadoghq.com/api/v0.2/traces"
  sample_rate: 0.1
  headers:
    DD-API-KEY: "${DD_API_KEY}"
```

## Honeycomb

Honeycomb accepts OTLP HTTP at `https://api.honeycomb.io:443/v1/traces`.
Authentication is the `x-honeycomb-team` header; an explicit dataset
header selects the dataset for the spans.

```yaml
tracing:
  otlp_endpoint: "https://api.honeycomb.io:443/v1/traces"
  sample_rate: 0.05
  headers:
    x-honeycomb-team: "${HONEYCOMB_API_KEY}"
    x-honeycomb-dataset: "fhir-subs"
```

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

Internal cluster addresses are not loopback; the `insecure: true`
toggle is required when TLS is terminated at an upstream ingress and
not at the collector itself. Prefer terminating TLS at the collector
and populating `tracing.tls.*` instead — the host-to-collector hop
carries operational telemetry that benefits from transport security.

## Grafana Tempo

Tempo's OTLP HTTP receiver is on port 4318 by default. Tempo Cloud
uses the same endpoint at
`https://tempo-prod-04-prod-us-east-0.grafana.net:443/v1/traces` with
basic auth carried in the `Authorization` header.

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

## mTLS to a private collector

When the collector lives on a private network and operators want
client-cert auth instead of a shared header, populate `tracing.tls.*`
and leave `insecure: false`. The binary's `buildTracingTLSConfig`
(in `internal/infra/observability/observability.go`) loads PEM files
from disk and builds a `*tls.Config` with the cert pair and CA bundle.

```yaml
tracing:
  otlp_endpoint: "https://collector.internal:4318/v1/traces"
  sample_rate: 0.1
  tls:
    cert_file: /etc/fhir-subs/otel/client.pem
    key_file:  /etc/fhir-subs/otel/client.key
    ca_file:   /etc/fhir-subs/otel/ca.pem
```

## Smoke test

The OTel HTTP middleware (`internal/api/handlers/tracing.go`) wraps
the public chi router; every request to a registered route produces a
span. After startup, exercise a route and confirm the span lands at
the back-end:

```bash
# /metadata is the public FHIR conformance endpoint and produces a
# span when the tracing middleware is wired.
curl -i -H 'Accept: application/fhir+json' \
  "http://${FHIR_SUBS_HOST}:${FHIR_SUBS_PORT}/metadata"
```

If no span appears:

1. Confirm `tracing.otlp_endpoint` is non-empty in the loaded config
   (the binary disables tracing entirely when it is empty). Run
   `fhir-subs --check-config` and grep for `tracing.otlp_endpoint` in
   the rendered effective config — empty means the no-op tracer is
   active.
2. Re-run with `--log-level debug`. The OTel SDK's batch-span-processor
   emits `OTLP exporter: ...` on transport errors via the global error
   handler; debug logging surfaces those.
3. Verify the network path from a sidecar: `nc -vz <collector-host>
   <collector-port>` from inside the pod.

The span queue and exporter health are not currently surfaced as
Prometheus metrics. Operators who need exporter-failure alerting
should run an OpenTelemetry Collector sidecar in front of the
back-end and alert on the collector's own
`otelcol_exporter_send_failed_spans` counter.

## Notes

- The exporter uses HTTP (`otlp/http`); gRPC is not currently wired
  in. Trace back-ends that only accept gRPC (e.g., AWS X-Ray native)
  need a collector-side translator (e.g., the OpenTelemetry Collector
  run as a sidecar).
- Sampling is head-based via `sdktrace.ParentBased(TraceIDRatioBased(rate))`.
  Tail sampling, if needed, runs in a collector upstream of the
  back-end, not in this service.
- The PHI-redaction layer at `tracing.SafeAttribute` (S-14.8) strips
  PHI-shaped values from span attributes before export. Custom span
  tags added in application code must call `SafeAttribute` before
  `span.SetAttributes`.
- An empty `otlp_endpoint` does not error — the module returns a
  no-op tracer and the HTTP middleware becomes a passthrough. This
  is the preferred way to disable tracing in development.
