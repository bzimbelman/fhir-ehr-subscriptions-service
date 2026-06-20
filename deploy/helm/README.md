# fhir-subs Helm chart

Helm chart for `fhir-ehr-subscriptions-service`. Deploys a multi-replica
Subscriptions server on Kubernetes with sane production defaults: HPA, PDB,
NetworkPolicy, External Secrets Operator integration, and a fail-fast
init-container that validates config + secret resolution before the main
container starts.

The chart targets Kubernetes >= 1.27. It does **not** ship a Postgres
sub-chart — see [Postgres](#postgres) below for the operator-managed
patterns we recommend.

## Quick start

```sh
# 1. Install your dependencies (one-time per cluster). See sections below.
#    - Postgres operator (CrunchyData PGO / Zalando / CloudNativePG)
#    - cert-manager (for TLS Secret on the API)
#    - External Secrets Operator (optional)
#    - Prometheus Operator + ServiceMonitor CRDs (optional)

# 2. Create a namespace.
kubectl create namespace fhir-subs

# 3. Create the at-rest encryption key + DB URL Secret (out of band).
kubectl -n fhir-subs create secret generic my-release-fhir-subs-secrets \
  --from-literal=DATABASE_URL='postgres://user:pass@db.example.com:5432/fhir?sslmode=require' \
  --from-file=at_rest_key=./at_rest_key.bin

# 4. Install the chart.
helm install my-release ./deploy/helm/fhir-subs \
  --namespace fhir-subs \
  --set image.tag=v0.1.0 \
  --set tls.existingSecret=fhir-subs-api-tls

# 5. Watch the pod become ready (init-container runs --check-config first).
kubectl -n fhir-subs rollout status deployment/my-release-fhir-subs

# 6. Probe readiness.
kubectl -n fhir-subs port-forward svc/my-release-fhir-subs 8081:8081
curl -fsS http://localhost:8081/readyz
```

## Architecture

The chart deploys one Deployment with the following resources:

- **Deployment** with rolling updates (`maxSurge=25%, maxUnavailable=0`) and
  termination grace period of 60s — long enough for in-flight HL7 messages
  to drain (B-31 scheduler drain semantics).
- **InitContainer** (`config-check`) that runs the binary with
  `--check-config` to fail-fast on bad config or unreadable secrets. The
  schema migration itself runs in the main container under a Postgres
  advisory lock (B-33), which is multi-replica safe — every pod tries to
  migrate, exactly one succeeds, the rest no-op.
- **Service** exposing four named ports:
  - `api` (8443) — Subscriptions HTTPS API (FHIR R4B/R5)
  - `probes` (8081) — `/healthz`, `/readyz`, `/startup`
  - `mllp` (2575) — HL7 v2 MLLP listener
  - `metrics` (9090) — Prometheus metrics
- **HorizontalPodAutoscaler** (v2) — CPU-based by default, memory optional.
- **PodDisruptionBudget** (`minAvailable: 1`) so cluster maintenance can
  never take all replicas down at once.
- **NetworkPolicy** locked to: ingress on the four named ports, egress to
  DNS + Postgres + any extra rules you supply for subscriber endpoints.
- **ServiceAccount** (no automount — the binary doesn't talk to the API
  server).
- **ConfigMap** with the rendered `config.yaml`, hashed into a pod
  annotation so rollouts pick up changes (in addition to the SIGHUP /
  mtime hot-reload paths in B-35).
- Optional **ExternalSecret**, **Ingress**, **ServiceMonitor**.

## Postgres

The chart expects an externally-managed Postgres. Recommended setups:

### CrunchyData Postgres Operator (PGO)

```yaml
# postgres-cluster.yaml
apiVersion: postgres-operator.crunchydata.com/v1beta1
kind: PostgresCluster
metadata:
  name: fhir-subs
spec:
  postgresVersion: 16
  instances:
    - name: instance1
      replicas: 2
      dataVolumeClaimSpec:
        accessModes: [ReadWriteOnce]
        resources:
          requests:
            storage: 20Gi
  backups:
    pgbackrest:
      repos:
        - name: repo1
          volume:
            volumeClaimSpec:
              accessModes: [ReadWriteOnce]
              resources:
                requests:
                  storage: 10Gi
```

Apply, then wire the chart:

```sh
kubectl apply -f postgres-cluster.yaml
# PGO creates a Secret named `<cluster>-pguser-<user>` with `uri` key.
helm install ... \
  --set externalSecrets.enabled=true \
  --set externalSecrets.data.DATABASE_URL.remoteRef.key=fhir-subs-pguser-fhir-subs \
  --set externalSecrets.data.DATABASE_URL.remoteRef.property=uri
```

### Zalando postgres-operator

Default `postgres-operator` provisions a Secret `<role>.<cluster>.credentials`
with `username` + `password`. Compose the URL via your own ExternalSecret
template, or wire the URL out-of-band.

### CloudNativePG (CNPG)

CNPG creates `<cluster>-app` Secret with `uri`. Same pattern as PGO.

The default NetworkPolicy egress selector matches PGO's primary-pod label
(`postgres-operator.crunchydata.com/role: master`). For Zalando / CNPG
override `networkPolicy.egress.postgres.podSelector` to match those
operators' primary labels.

### Managed Postgres (RDS, Cloud SQL)

Set `networkPolicy.egress.postgres.enabled=false` and add an
`extraRules` egress entry that allows traffic to the RDS endpoint /
private subnet, e.g.:

```yaml
networkPolicy:
  egress:
    postgres:
      enabled: false
    extraRules:
      - to:
          - ipBlock:
              cidr: 10.10.0.0/16
        ports:
          - port: 5432
            protocol: TCP
```

## Secret management

The service consumes secrets through two paths driven by the config
loader's placeholder forms:

- `${env:VAR}` — resolved from a container env var.
- `${file:/path}` — resolved by reading the file at the given path. The
  config loader ALSO mtime-polls these paths so vault-agent /
  cert-manager / ESO rotations trigger an in-process reload (B-35).

The default `values.yaml` wires `DATABASE_URL` as an env var and
`at_rest_key` as a file under `/etc/fhir-subs/secrets/`. Both come out
of a single Secret named `<release>-fhir-subs-secrets` by default.

### External Secrets Operator (recommended)

```yaml
externalSecrets:
  enabled: true
  secretStoreRef:
    name: my-cluster-secret-store
    kind: ClusterSecretStore
  refreshInterval: 1h
  data:
    DATABASE_URL:
      remoteRef:
        key: prod/fhir-subs/db
        property: url
    at_rest_key:
      remoteRef:
        key: prod/fhir-subs/at-rest-key
```

The chart will create an `ExternalSecret` that ESO reconciles into the
Secret consumed by the pod.

### Existing Secret (no ESO)

If you manage Secrets out of band (sealed-secrets, sops-nix, plain
`kubectl create secret`):

```yaml
externalSecrets:
  enabled: false
secrets:
  existingSecret: my-existing-secret
```

The Secret must contain a key for every `secrets.env[].key` and
`secrets.files[].key` referenced in `values.yaml`.

## TLS

The Subscriptions API serves HTTPS directly. cert-manager pattern:

```yaml
# certificate.yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: fhir-subs-api-tls
spec:
  secretName: fhir-subs-api-tls
  issuerRef:
    name: letsencrypt-prod
    kind: ClusterIssuer
  dnsNames:
    - fhir-subs.example.com
```

Then:

```yaml
tls:
  enabled: true
  existingSecret: fhir-subs-api-tls
```

The chart mounts the Secret read-only at `/etc/fhir-subs/tls/`. Update
`config.contents.server.http.tls.cert_file` / `.key_file` if you change
the mount path.

## Multi-arch image

The Dockerfile is multi-arch. Build with buildx:

```sh
docker buildx create --use --name fhir-subs-builder
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --tag ghcr.io/bzimbelman/fhir-ehr-subscriptions-service:v0.1.0 \
  --push \
  .
```

CI (P3.6) wires the same command. For local-dev arm64 builds:

```sh
docker buildx build --platform linux/arm64 --load --tag fhir-subs:dev .
```

## Validation

Lint and template smoke:

```sh
helm lint deploy/helm/fhir-subs
helm template my-release deploy/helm/fhir-subs --debug | kubectl apply --dry-run=server -f -
```

Install on `kind`:

```sh
kind create cluster --name fhir-subs-test
# install Postgres operator + CRDs first (steps elided)
helm install my-release deploy/helm/fhir-subs -n fhir-subs --create-namespace
kubectl -n fhir-subs wait --for=condition=Ready pod -l app.kubernetes.io/name=fhir-subs --timeout=300s
kubectl -n fhir-subs port-forward svc/my-release-fhir-subs 8081:8081 &
curl -fsS http://localhost:8081/readyz
```

## Values reference

See [`values.yaml`](fhir-subs/values.yaml) for the canonical, commented
reference. Key sections:

| Path | Type | Default | Purpose |
|---|---|---|---|
| `replicaCount` | int | 2 | Replicas when HPA is disabled. |
| `image.repository` | string | `<your-org>/...` (placeholder; **required**) | Image to deploy. Helm template fails until the operator overrides this (OP #123). |
| `image.tag` | string | `""` (=appVersion) | Image tag override. |
| `image.digest` | string | `""` | Pin to a sha256 digest (overrides tag). |
| `service.apiPort` | int | 8443 | Subscriptions API port. |
| `service.mllpPort` | int | 2575 | HL7 v2 MLLP listener port. |
| `resources.{requests,limits}` | object | 200m/256Mi - 2/2Gi | Per-pod resources. |
| `probes.{liveness,readiness,startup}` | object | sane defaults | Probe tunables. |
| `autoscaling.enabled` | bool | true | Toggle HPA. |
| `autoscaling.{min,max}Replicas` | int | 2 / 6 | HPA bounds. |
| `autoscaling.targetCPUUtilizationPercentage` | int | 75 | CPU HPA target. |
| `podDisruptionBudget.minAvailable` | int | 1 | PDB. |
| `networkPolicy.enabled` | bool | true | Toggle NetworkPolicy. |
| `networkPolicy.ingress.api.from` | list | `[]` (**required**) | Operator MUST list at least one peer. Empty list fails template (OP #122). |
| `networkPolicy.ingress.mllp.from` | list | `[]` (**required**) | Operator MUST list at least one peer. Empty list fails template (OP #122). |
| `networkPolicy.egress.postgres.podSelector` | object | PGO master | Match your PG primary. |
| `networkPolicy.egress.extraRules` | list | `[]` | Extra egress (e.g., subscriber endpoints). |
| `config.contents` | string | minimal dev config | Inline service config. |
| `externalSecrets.enabled` | bool | false | Create ExternalSecret. |
| `externalSecrets.data` | map | `DATABASE_URL`, `at_rest_key` | ESO key map. |
| `secrets.existingSecret` | string | `""` | Use a pre-created Secret. |
| `tls.enabled` | bool | true | Mount TLS Secret. |
| `tls.existingSecret` | string | `""` | Existing `kubernetes.io/tls` Secret. |
| `configCheck.enabled` | bool | true | InitContainer fail-fast on bad config. Renamed from `migrationInit` (OP #125) — the container does NOT run migrations. |
| `metrics.enabled` | bool | false | Expose `/metrics` Service port and allow ServiceMonitor (OP #121). Leave off until the binary opens its Prometheus listener. |
| `serviceMonitor.enabled` | bool | false | Prometheus Operator scraping. Requires `metrics.enabled=true`. |
| `affinity` | object | podAntiAffinity / hostname | Spread replicas across nodes. |

## Upgrades

Schema migrations run on first pod start under an advisory lock. During a
rolling upgrade, all replicas attempt the migration; exactly one wins and
the others see "already applied" and proceed. There is no separate
"migration job" — the bundled init-container only validates config; it
does not run migrations. This matches the operator audit's recommendation
(B-33) and avoids the failure mode where a one-off Job races a still-
running old replica.

If you need a strictly-ordered migration (rare — only when a column is
removed and the old replicas would error reading it), drain to one replica
before upgrading, deploy the new image, then scale back up.

## Uninstall

```sh
helm -n fhir-subs uninstall my-release
# The Secret is annotated `helm.sh/resource-policy: keep` so it survives
# uninstall. Delete manually if you intend to free its name.
kubectl -n fhir-subs delete secret my-release-fhir-subs-secrets
```
