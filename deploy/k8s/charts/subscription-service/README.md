# subscription-service Helm chart

Helm chart for deploying the subscription-service HL7v2/FHIR pipeline to Kubernetes. Mirrors the four-container Docker Compose stack at `deploy/docker/docker-compose.yml`:

| Workload | Image (default) | Purpose |
|----------|----------------|---------|
| `<release>-postgres` (StatefulSet) | `postgres:16-alpine` | HAPI's datastore |
| `<release>-hapi` (Deployment) | `subscription-service/hapi:dev` | FHIR R4 server + Subscriptions engine |
| `<release>-matchbox` (Deployment) | `europe-west6-docker.pkg.dev/ahdis-ch/ahdis/matchbox:v3.9.13` | `$transform` for HL7v2 -> FHIR |
| `<release>-interface-engine` (Deployment) | `subscription-service/interface-engine:dev` | HL7 MLLP listener + Camel router |

A FHIR HTTPS Ingress fronts HAPI; a separate `LoadBalancer` Service exposes the interface engine's MLLP listener on TCP 2575.

## Tickets

- **#363** — Helm chart scaffold for k8s (this directory).
- **#364** — Validate the chart on Rancher Desktop.

## Quick start (Rancher Desktop)

```bash
# 1. Verify you're pointed at the right cluster.
kubectl config current-context        # -> rancher-desktop

# 2. Build the locally-derived images.
docker build -t subscription-service/hapi:dev             hapi/
docker build -t subscription-service/interface-engine:dev interface-engine/

# 3. Load images into the cluster runtime.
#    Rancher Desktop has two container engine modes:
#      a) "dockerd (moby)"   — k3s uses dockerd directly. NO load step
#                              needed; `docker build` already populates the
#                              cluster's image store. (This is the default
#                              and what `kubectl get nodes -o wide` will
#                              show as `docker://X.Y.Z` under CONTAINER-
#                              RUNTIME.)
#      b) "containerd"       — k3s and dockerd have separate stores. Load:
#                              docker save subscription-service/hapi:dev             | nerdctl --namespace k8s.io load
#                              docker save subscription-service/interface-engine:dev | nerdctl --namespace k8s.io load
#
#    Either way, `imagePullPolicy: IfNotPresent` in values.yaml prevents
#    the kubelet from trying to pull a non-existent registry tag.

# 4. Install.
helm install subsvc deploy/k8s/charts/subscription-service \
  -n subsvc-test --create-namespace \
  -f deploy/k8s/charts/subscription-service/values-rancher.yaml

# 5. Wait for all four workloads.
kubectl -n subsvc-test rollout status statefulset/subsvc-postgres --timeout=300s
kubectl -n subsvc-test rollout status deployment/subsvc-matchbox  --timeout=300s
kubectl -n subsvc-test rollout status deployment/subsvc-hapi      --timeout=600s
kubectl -n subsvc-test rollout status deployment/subsvc-interface-engine --timeout=300s

# 6. Add a hosts entry so the ingress hostname resolves locally.
echo "127.0.0.1 subscription-service.local" | sudo tee -a /etc/hosts

# 7. Smoke test.
curl -fsS http://subscription-service.local/fhir/metadata | jq .resourceType
# -> "CapabilityStatement"

# 8. Teardown.
helm uninstall subsvc -n subsvc-test
kubectl delete ns subsvc-test
```

## Values

The full schema lives in [`values.yaml`](values.yaml); the highlights:

### Images

```yaml
image:
  hapi:     { repository: subscription-service/hapi,    tag: dev, pullPolicy: IfNotPresent }
  matchbox: { repository: europe-west6-docker.pkg.dev/ahdis-ch/ahdis/matchbox, tag: v3.9.13 }
  interfaceEngine: { repository: subscription-service/interface-engine, tag: dev, pullPolicy: IfNotPresent }
  postgres: { repository: postgres,        tag: "16-alpine" }
  igFetcher:{ repository: curlimages/curl, tag: "8.10.1" }   # initContainer that fetches IGs
```

`pullPolicy: IfNotPresent` is the default so Rancher Desktop / kind / minikube can use locally-loaded images without pushing to a registry. For a real cluster, push to your registry and set `pullPolicy: Always`.

### Postgres

```yaml
postgres:
  user: hapi
  password: ""                # empty -> chart auto-generates a 32-char
                              # random password; preserved across upgrades.
  database: hapi
  storage:
    size: 10Gi
    storageClassName: ""      # "" -> cluster default
```

The chart rolls its own minimal Postgres StatefulSet (not the bitnami subchart) so the Compose and k8s targets stay identical down to the image and PGDATA layout. The PVC is templated by `volumeClaimTemplates` and survives `helm uninstall`.

#### Postgres password (ticket #419)

The chart resolves the Postgres password in this precedence:

1. **Explicit `postgres.password` value** — wins if set to a non-empty string. Recommended path is layering it in via a sealed/external secret manager, not committing it in a values file.
2. **Existing Secret in the namespace** — if `postgres.password` is empty AND a Secret named `<release>-postgres` already exists, the chart re-reads the `POSTGRES_PASSWORD` key from it. This is the upgrade path: the password stays stable for the life of the install.
3. **Auto-generated random 32-char password** — first install only, when neither of the above applies. Uses Helm's `randAlphaNum 32`. This means no operator can accidentally ship the old `hapi` literal default to a real cluster.

For **secure production** deploys, do not rely on the auto-generated password — back it with durable secret management:

- Use an external secret manager (sealed-secrets, external-secrets, SOPS) and supply the password via `--set postgres.password=...` or by pre-creating the `<release>-postgres` Secret before `helm install`.
- Or set `externalPostgres.enabled: true` (ticket #416, parallel work) to point at a managed Postgres outside the chart entirely.

**Caveat on `helm template`**: rendering offline (no cluster) skips the `lookup` step, so each `helm template` run prints a *different* generated password. The stable-across-upgrade behavior is a server-side property of `helm install`/`upgrade`.

### External Postgres (ticket #416)

For production deployments operators usually want to point HAPI at a managed Postgres (RDS, Cloud SQL, Azure DB for PostgreSQL, on-prem, etc.) rather than running the chart's in-cluster StatefulSet. Flip `externalPostgres.enabled: true` and the chart **skips** the StatefulSet, headless Service, PVC, and Secret entirely; HAPI is configured to connect to the host you specify.

```yaml
externalPostgres:
  enabled: true
  host: subsvc-prod.abc123.us-east-1.rds.amazonaws.com
  port: 5432
  database: hapi
  user: hapi
  passwordSecret: subsvc-prod-postgres-pw       # must EXIST before helm install
  sslMode: require                              # disable | require | verify-ca | verify-full
```

The Secret named in `passwordSecret` must exist in the release namespace **before** `helm install` and hold the password under the key `password`. Pre-create it with `kubectl`:

```bash
kubectl create secret generic subsvc-prod-postgres-pw \
  --from-literal=password='<the-password>' \
  --namespace=subscription-service
```

#### Schema prerequisites

HAPI auto-creates its JPA tables on first connect. The DB user therefore needs **CREATE** privileges on the target database. On managed Postgres services the canonical recipe is:

1. Create the database (e.g. `hapi`).
2. Create a role (e.g. `hapi`).
3. `GRANT ALL PRIVILEGES ON DATABASE hapi TO hapi;`
4. Set that role and the password in the values block + Secret above.

The RDS console, Cloud SQL UI, and Azure portal all expose this through their "create role" / "create user" forms.

#### Worked example: RDS + AWS Secrets Manager

Common production pattern — the DB password lives in AWS Secrets Manager, and you mirror it into a Kubernetes Secret at deploy time:

```bash
# 1. Read the password out of AWS Secrets Manager.
POSTGRES_PASSWORD=$(aws secretsmanager get-secret-value \
  --secret-id subsvc-prod/postgres \
  --query SecretString --output text)

# 2. Mirror it into a Kubernetes Secret in the release namespace.
kubectl create secret generic subsvc-prod-postgres-pw \
  --from-literal=password="$POSTGRES_PASSWORD" \
  --namespace=subscription-service

# 3. Install the chart pointed at RDS.
helm upgrade --install subsvc deploy/k8s/charts/subscription-service \
  -n subscription-service --create-namespace \
  -f deploy/k8s/charts/subscription-service/values-prod.yaml \
  --set externalPostgres.enabled=true \
  --set externalPostgres.host=subsvc-prod.abc123.us-east-1.rds.amazonaws.com \
  --set externalPostgres.database=hapi \
  --set externalPostgres.user=hapi \
  --set externalPostgres.passwordSecret=subsvc-prod-postgres-pw \
  --set externalPostgres.sslMode=verify-full
```

For a fully GitOps flow, replace step 2 with [external-secrets](https://external-secrets.io/) so the Kubernetes Secret syncs automatically from AWS Secrets Manager (or GCP Secret Manager / Vault / Azure Key Vault / etc.).

#### Validation

`externalPostgres.enabled: true` is rejected at template time if `host` or `passwordSecret` is empty:

```
Error: execution error at (subscription-service/templates/validations.yaml:7:4):
externalPostgres.enabled is true but externalPostgres.host is empty. Set it to
your managed Postgres hostname.
```

This means `helm install` fails immediately rather than producing a Deployment that CrashLoops on JDBC connect.

### IGs (US Core + Subscriptions Backport)

IG tarballs are **not** baked into ConfigMaps — US Core 7.0 is ~1.6 MB, and the per-key ConfigMap limit is 1 MiB. Instead each pod (HAPI, Matchbox) runs an `initContainer` that downloads the configured IGs from the FHIR package registry into an `emptyDir` mounted at `/app/igs`:

```yaml
hapi:
  igs:
    - name: hl7.fhir.us.core
      version: "7.0.0"
    - name: hl7.fhir.uv.subscriptions-backport.r4
      version: "1.1.0"
  igRegistry: https://packages.fhir.org
matchbox:
  igs:
    - name: hl7.fhir.uv.v2mappings
      version: "1.0.0"
  igRegistry: https://packages.fhir.org
```

**The init containers need internet egress** to `packages.fhir.org`. For air-gapped clusters, mirror the packages somewhere internal and override `igRegistry`.

### Feature toggles

```yaml
featureToggles:
  auth:
    enabled: false        # default OFF so the chart works without a Keycloak
    issuer: ""            # e.g. https://keycloak.example.com/realms/subsvc
    jwksUrl: ""           # blank -> <issuer>/protocol/openid-connect/certs
  validation:
    mode: "off"           # off | warn | enforce  (ticket #367)
  channelSecurity:
    mode: strict          # strict | relaxed | permissive  (ticket #368)
  multitenancy:
    mode: disabled        # disabled | enabled  (ticket #369)
    tenantClaim: tenant
    testMode: false
```

These map to the same `SUBSCRIPTION_SERVICE_*` env vars as the Compose stack; see [`docs/architecture.md`](../../../../docs/architecture.md) and [`docs/multi-tenancy.md`](../../../../docs/multi-tenancy.md) for the behavior of each mode.

### Ingress

```yaml
ingress:
  enabled: true
  className: traefik
  hosts:
    - host: subscription-service.local
      paths: [{ path: /, pathType: Prefix }]
  tls: []                 # add a TLS block in values-dev/prod for real envs
```

Routes `/` (and therefore `/fhir/*`) at the HAPI Service. The HAPI tester UI at `/` is intentionally reachable for ops convenience; restrict the path list if you don't want it.

### TLS (cert-manager)

Cloud k8s deployments typically auto-provision TLS via [cert-manager](https://cert-manager.io/) and an ACME issuer (Let's Encrypt, ZeroSSL, internal CA). The chart supports this with a single toggle that adds the right annotation to the Ingress and auto-populates the `tls` block so cert-manager knows which Secret to write the issued cert to.

```yaml
ingress:
  enabled: true
  hosts:
    - host: subscription-service.example.com
      paths: [{ path: /, pathType: Prefix }]
  certManager:
    enabled: true
    clusterIssuer: letsencrypt-prod      # cluster-scoped ClusterIssuer (typical)
    # issuer: my-namespace-issuer        # OR a namespaced Issuer; not both
```

What that renders:

- Annotation `cert-manager.io/cluster-issuer: letsencrypt-prod` on the Ingress (or `cert-manager.io/issuer: <name>` if you set `issuer` instead).
- Auto-populated TLS block when `ingress.tls` is empty:
  ```yaml
  tls:
    - hosts:
        - subscription-service.example.com
      secretName: <release>-hapi-tls
  ```
  cert-manager watches the Ingress, requests a cert from the issuer, and writes it to `<release>-hapi-tls` in the same namespace.

**Prerequisite — cert-manager is NOT installed by this chart.** It must already be running in the cluster, with a `ClusterIssuer` (or namespace-scoped `Issuer`) configured for the chosen ACME / DNS-01 provider. Most managed clusters install it once at the platform layer:

```bash
helm repo add jetstack https://charts.jetstack.io
helm install cert-manager jetstack/cert-manager \
  -n cert-manager --create-namespace \
  --set installCRDs=true
# then apply your ClusterIssuer (letsencrypt-prod, letsencrypt-staging, etc.)
```

**Precedence rules:**

- If both `clusterIssuer` and `issuer` are set, `clusterIssuer` wins.
- If `certManager.enabled: true` but neither is set, the chart fails at template time with a clear error rather than rendering a broken Ingress.
- If the operator supplies their own `ingress.tls` block, it is **not** overridden — cert-manager integration only auto-populates when `tls` is empty. This lets you bring your own Secret name or list multiple cert/host groupings if needed.

**Trade-off — when not to use this:**

Operators who already manage TLS via an external secret manager (sealed-secrets, external-secrets, SOPS-encrypted Secrets, manual upload) should leave `certManager.enabled: false` and populate `ingress.tls` directly:

```yaml
ingress:
  tls:
    - hosts: [subscription-service.example.com]
      secretName: subscription-service-tls    # pre-existing Secret in the namespace
```

The two paths are mutually exclusive; pick whichever matches your platform's TLS story.

### MLLP (port 2575)

```yaml
interfaceEngine:
  mllp:
    service:
      type: LoadBalancer
      port: 2575
      loadBalancerIP: ""   # set on cloud LBs; ignored by klipper-lb
```

A second Service (`<release>-interface-engine-mllp`) exposes the interface engine's MLLP listener as a `LoadBalancer`. On Rancher Desktop, klipper-lb binds it to the host on `localhost:2575`. In cloud clusters, the cloud provider's LB controller picks up a public IP.

### Pod Security Standards (ticket #420)

The chart's default `podSecurityContext` and `securityContext` blocks satisfy the [Pod Security Standards](https://kubernetes.io/docs/concepts/security/pod-security-standards/) `restricted` profile — the strictest of the three profiles, enforced by default on GKE Autopilot, OpenShift, and many hardened production clusters. Concretely, every Pod template renders with:

- `runAsNonRoot: true` and a non-zero `runAsUser`
- `allowPrivilegeEscalation: false`
- `capabilities.drop: [ALL]`
- `seccompProfile.type: RuntimeDefault`

Each workload uses the UID its image actually expects (so the kubelet doesn't refuse to chown emptyDir / PVC mounts):

| Workload | Image | UID |
|----------|-------|-----|
| `hapi` | `hapiproject/hapi:v7.6.0` (distroless) | 65532 |
| `matchbox` | `ahdis/matchbox:v3.9.13` | 1000 |
| `interface-engine` | `subscription-service/interface-engine:dev` | 10001 |
| `postgres` | `postgres:16-alpine` | 70 |
| `fetch-igs` (initContainer) | `curlimages/curl:8.10.1` | 100 |

Verify on your cluster by labeling a fresh namespace and installing:

```bash
kubectl create namespace psa-test
kubectl label namespace psa-test \
  pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/audit=restricted   \
  pod-security.kubernetes.io/warn=restricted

helm install psatest deploy/k8s/charts/subscription-service \
  -n psa-test \
  -f deploy/k8s/charts/subscription-service/values-rancher.yaml \
  --set ingress.enabled=false

kubectl -n psa-test wait --for=condition=Ready pod --all --timeout=420s
helm uninstall psatest -n psa-test
kubectl delete ns psa-test
```

If any pod fails to schedule under `restricted`, the chart's `scripts/k8s/test-psa.sh` script renders the chart and validates every pod template / container — run it locally to triage offline.

To temporarily loosen for debugging (e.g. `kubectl exec` as root), override on the CLI:

```bash
# DO NOT do this in production.
helm upgrade ... \
  --set podSecurityContext.runAsNonRoot=false \
  --set securityContext.allowPrivilegeEscalation=true \
  --set securityContext.runAsNonRoot=false
```

The per-workload override pattern is documented at the top of [`values.yaml`](values.yaml) — chart-level keys deep-merge with `<workload>.podSecurityContext` / `<workload>.securityContext` so you can pin a single workload to a different UID without rewriting the chart-level defaults.

### NetworkPolicy

Off by default (`networkPolicy.enabled: false`) because k3s/Rancher Desktop ships without a NetworkPolicy controller. Turn on in production clusters running Calico / Cilium / etc.

### Pod disruption budgets

Off by default (`podDisruptionBudgets.enabled: false`) for the same reason as NetworkPolicy — k3s / Rancher Desktop doesn't ship a PDB controller, so the manifests would be inert at best and confusing at worst. Turn on in real clusters (`values-dev.yaml` already does) to protect each workload from voluntary evictions during node drains, k8s control-plane upgrades, and scale-down events. Forced drains are not blocked.

```yaml
podDisruptionBudgets:
  enabled: false                  # flip to true in real clusters
  hapi:             { minAvailable: 1 }
  matchbox:         { minAvailable: 1 }
  interfaceEngine:  { minAvailable: 1 }
  postgres:         { maxUnavailable: 0 }   # StatefulSet, single replica
```

Each workload accepts either `minAvailable` or `maxUnavailable` (the PDB API allows only one of the two — `minAvailable` wins if both are set). The defaults pair with the chart's single-replica deployments; a `minAvailable: 1` PDB on a single-replica deployment blocks all voluntary evictions of that pod, so coordinate with cluster admins before opting in on maintenance-heavy environments. Bump `replicaCount` first if you want graceful rolling drains.

### Monitoring (ServiceMonitor)

Opt-in `ServiceMonitor` resources for the [Prometheus Operator](https://prometheus-operator.dev/). Disabled by default so the chart installs cleanly on clusters without it.

```yaml
monitoring:
  enabled: false                # flip to true on clusters running the Operator
  serviceMonitor:
    interval: 30s
    scrapeTimeout: 10s
    labels: {}                  # e.g. { release: prometheus } so the Operator's selector picks it up
    path: /actuator/prometheus  # Spring Boot Actuator's Prometheus endpoint
```

Two `ServiceMonitor` objects render when enabled (one per workload that will expose `/actuator/prometheus`):

- `<release>-interface-engine` — scrapes the interface engine on its `http` port (8090)
- `<release>-hapi` — scrapes HAPI on its `http` port (8080)

Two layers of safety:

1. **Toggle** — `monitoring.enabled: false` by default.
2. **Capability gate** — the template additionally checks `.Capabilities.APIVersions.Has "monitoring.coreos.com/v1"`. If the Prometheus Operator CRDs aren't installed, the block is a no-op even when `enabled: true`, so flipping the toggle never breaks a cluster that doesn't have the Operator. (Note: `helm template` without `--api-versions monitoring.coreos.com/v1` won't render the resources either, since it has no live cluster to query.)

> **Dependency: Epic #387 ticket #389.** The actual `/actuator/prometheus` endpoint isn't wired up on either workload yet — that ships with #389. Until then, enabling this block produces a `ServiceMonitor` that points at a 404. The chart plumbing is in place so flipping `enabled: true` AFTER #389 lands needs zero chart changes. Tracked as ticket #418.

Rendering with `--api-versions` to fake the capability for `helm template`:

```bash
helm template subsvc deploy/k8s/charts/subscription-service \
  --set monitoring.enabled=true \
  --set monitoring.serviceMonitor.labels.release=prometheus \
  --api-versions monitoring.coreos.com/v1
# -> 2 ServiceMonitor resources in the output
```

## Values overlays

Three overlays ship with the chart:

| File | Purpose |
|------|---------|
| [`values.yaml`](values.yaml) | Defaults; safe for any cluster |
| [`values-dev.yaml`](values-dev.yaml) | Template for a real dev cluster: auth ON, ingress TLS, larger PVC. Copy and edit the placeholder hosts. |
| [`values-rancher.yaml`](values-rancher.yaml) | Rancher Desktop validation: auth OFF, permissive channel security, smaller PVC, `subscription-service.local` host |

Layer them with `-f values-rancher.yaml`.

## Acceptance checks

```bash
# Render with each overlay.
helm template deploy/k8s/charts/subscription-service
helm template deploy/k8s/charts/subscription-service -f deploy/k8s/charts/subscription-service/values-rancher.yaml
helm template deploy/k8s/charts/subscription-service -f deploy/k8s/charts/subscription-service/values-dev.yaml

# Lint.
helm lint deploy/k8s/charts/subscription-service

# Dry-run apply against the cluster.
helm template subsvc deploy/k8s/charts/subscription-service \
  -f deploy/k8s/charts/subscription-service/values-rancher.yaml \
| kubectl apply --dry-run=client -f -
```

## See also

- [`docs/architecture.md`](../../../../docs/architecture.md) — overall design; the "Kubernetes (Helm)" section is the contract this chart implements.
- [`docs/k8s-deployment.md`](../../../../docs/k8s-deployment.md) — operator workflow: building, loading images, installing, upgrading, troubleshooting.
- [`deploy/docker/docker-compose.yml`](../../../docker/docker-compose.yml) — the equivalent Docker Compose stack.
