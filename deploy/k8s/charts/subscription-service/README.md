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
  password: hapi              # CHANGE in values-prod.yaml or via sealed secret.
  database: hapi
  storage:
    size: 10Gi
    storageClassName: ""      # "" -> cluster default
```

The chart rolls its own minimal Postgres StatefulSet (not the bitnami subchart) so the Compose and k8s targets stay identical down to the image and PGDATA layout. The PVC is templated by `volumeClaimTemplates` and survives `helm uninstall`.

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

### NetworkPolicy

Off by default (`networkPolicy.enabled: false`) because k3s/Rancher Desktop ships without a NetworkPolicy controller. Turn on in production clusters running Calico / Cilium / etc.

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
