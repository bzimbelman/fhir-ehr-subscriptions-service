# Kubernetes (Helm) deployment

Operator workflow for the `subscription-service` Helm chart at [`deploy/k8s/charts/subscription-service/`](../deploy/k8s/charts/subscription-service/).

See [`docs/architecture.md`](architecture.md) "Kubernetes (Helm)" for the design rationale and [`deploy/k8s/charts/subscription-service/README.md`](../deploy/k8s/charts/subscription-service/README.md) for the per-value reference.

---

## What the chart deploys

| Workload | Kind | Default image | Notes |
|----------|------|---------------|-------|
| `<release>-postgres` | StatefulSet (1 replica) + PVC | `postgres:16-alpine` | HAPI's datastore. Headless Service. |
| `<release>-hapi` | Deployment (1 replica) | `subscription-service/hapi:dev` | FHIR R4 server. ClusterIP Service. |
| `<release>-matchbox` | Deployment (1 replica) | `europe-west6-docker.pkg.dev/ahdis-ch/ahdis/matchbox:v3.9.13` | `$transform`. ClusterIP Service. |
| `<release>-interface-engine` | Deployment (1 replica) | `subscription-service/interface-engine:dev` | HL7 MLLP listener. Two Services: ClusterIP for HTTP, LoadBalancer for MLLP. |

Plus: two ConfigMaps (HAPI `application.yaml` + JEP-330 healthcheck), two Secrets (Postgres creds + auth config), and an Ingress fronting HAPI.

---

## Rancher Desktop (local validation, ticket #364)

This is the path the chart was validated on. See the chart README for a copy-pastable quick start; this section is the operator-facing detail.

### Prerequisites

```bash
kubectl config current-context   # must be: rancher-desktop
helm version --short             # tested with v3.x / v4.x
docker version                   # tested with 29.x
```

`kubectl get nodes -o wide` shows the container runtime. Rancher Desktop has two modes:

- **dockerd (moby)** — k3s uses dockerd directly. Locally-built docker images are visible to k8s with no extra step. `CONTAINER-RUNTIME` shows `docker://...`.
- **containerd** — k3s uses its own containerd. Locally-built docker images must be re-loaded:

  ```bash
  docker save subscription-service/hapi:dev             | nerdctl --namespace k8s.io load
  docker save subscription-service/interface-engine:dev | nerdctl --namespace k8s.io load
  ```

  `CONTAINER-RUNTIME` shows `containerd://...`.

The chart sets `imagePullPolicy: IfNotPresent` so neither mode tries to pull a non-existent registry tag.

### Build the locally-derived images

```bash
docker build -t subscription-service/hapi:dev             hapi/
docker build -t subscription-service/interface-engine:dev interface-engine/
```

The HAPI image layers our auth/validation/channel-security/multi-tenancy JAR onto `hapiproject/hapi:v7.6.0`. The interface-engine image is built from the Gradle Spring Boot project in `interface-engine/`.

### Install

```bash
helm install subsvc deploy/k8s/charts/subscription-service \
  -n subsvc-test --create-namespace \
  -f deploy/k8s/charts/subscription-service/values-rancher.yaml
```

The chart's init containers will fetch the FHIR IGs from `packages.fhir.org` into an `emptyDir` mounted at `/app/igs`. The `values-rancher.yaml` overlay sets `igFetcherInsecure: true` because Rancher Desktop on a corporate-managed Mac (Netskope / Zscaler / etc.) intercepts TLS; without this flag the init container fails with `curl: (60) SSL certificate problem`. Skipping verification at this step is acceptable: the IGs are content-addressable by version and pinned in `values.yaml`, and runtime HAPI traffic never goes through the init-container TLS path.

### Wait for readiness

```bash
kubectl -n subsvc-test rollout status statefulset/subsvc-postgres --timeout=300s
kubectl -n subsvc-test rollout status deployment/subsvc-matchbox  --timeout=300s
kubectl -n subsvc-test rollout status deployment/subsvc-hapi      --timeout=600s
kubectl -n subsvc-test rollout status deployment/subsvc-interface-engine --timeout=300s
```

HAPI takes the longest (1-3 minutes on a fresh DB) because it has to install the US Core + Subscriptions Backport IGs.

### Hosts entry

The chart's default Ingress host is `subscription-service.local`. Rancher Desktop's traefik listens on `localhost:80`, but you'll need a hosts entry so the browser/curl sends the right `Host:` header:

```bash
echo "127.0.0.1 subscription-service.local" | sudo tee -a /etc/hosts
```

Or pass `-H "Host: subscription-service.local"` on every curl, as the smoke test below does.

### Smoke test

```bash
# 1. CapabilityStatement
curl -sS -H "Host: subscription-service.local" http://localhost/fhir/metadata \
  | jq '{resourceType, fhirVersion, software:.software.name, resourceCount:(.rest[0].resource|length)}'
# Expected:
#   { "resourceType": "CapabilityStatement",
#     "fhirVersion": "4.0.1",
#     "software": "HAPI FHIR Server",
#     "resourceCount": 146 }

# 2. POST a Patient, GET it back
curl -sS -H "Host: subscription-service.local" \
  -H "Content-Type: application/fhir+json" \
  -X POST -d '{"resourceType":"Patient","name":[{"family":"Doe","given":["Jane"]}]}' \
  http://localhost/fhir/Patient | jq '.id'

curl -sS -H "Host: subscription-service.local" http://localhost/fhir/Patient/<id> \
  | jq '{id, resourceType, name}'

# 3. MLLP round-trip (ADT^A04 -> AA ACK)
{ printf '\x0b'; printf 'MSH|^~\\&|TESTAPP|TESTFAC|HAPI|HOSP|20260626120000||ADT^A04|MSG00001|P|2.5\rEVN||20260626120000\rPID|1||MRN12345^^^HOSP^MR||Smith^John||19800101|M\rPV1|1|O|||||\r'; printf '\x1c\r'; } \
  | nc -w 5 localhost 2575 | xxd | head -5
# Expected: bytes show "MSA|AA|MSG00001" — Application Accept.
```

### Teardown

```bash
helm uninstall subsvc -n subsvc-test
kubectl delete namespace subsvc-test
```

The PVC for Postgres is removed with the namespace; data is gone.

---

## Production / dev clusters

The same pattern works for any dev or production cluster:

1. Push the locally-built images to a registry. Any OCI registry works (Docker Hub, ECR, GCR, GAR, ACR, Harbor, Quay, etc.):

   ```bash
   docker tag  subscription-service/hapi:dev               your-registry.example.com/subscription-service-hapi:<tag>
   docker push your-registry.example.com/subscription-service-hapi:<tag>
   docker tag  subscription-service/interface-engine:dev   your-registry.example.com/subscription-service-interface-engine:<tag>
   docker push your-registry.example.com/subscription-service-interface-engine:<tag>
   ```

2. Set the registry coordinates and pull policy in `values-dev.yaml` / `values-prod.yaml`:

   ```yaml
   image:
     hapi:
       repository: your-registry.example.com/subscription-service-hapi
       tag: <tag>
       pullPolicy: Always
     interfaceEngine:
       repository: your-registry.example.com/subscription-service-interface-engine
       tag: <tag>
       pullPolicy: Always
   imagePullSecrets:
     - name: your-registry-cred
   ```

   If the registry needs credentials, create the pull secret once per namespace with the
   standard kubectl recipe:

   ```bash
   kubectl create secret docker-registry your-registry-cred \
     --docker-server=your-registry.example.com \
     --docker-username=<user> \
     --docker-password=<password> \
     --docker-email=<email> \
     -n subscription-service
   ```

   Drop the `imagePullSecrets` block entirely if you're pulling from a public registry.

3. Install / upgrade:

   ```bash
   helm upgrade --install subsvc deploy/k8s/charts/subscription-service \
     -n subscription-service --create-namespace \
     -f deploy/k8s/charts/subscription-service/values-dev.yaml
   ```

4. Wait for rollout, then verify CapabilityStatement on the public hostname:

   ```bash
   curl -fsS https://subscription-service.example.com/fhir/metadata | jq .fhirVersion
   ```

5. Configure auth (`featureToggles.auth.issuer` -> `https://your-keycloak.example.com/realms/subscription-service`) and feature toggles per environment.

6. **For cloud deployments, point HAPI at a managed Postgres** (RDS, Cloud SQL, Azure DB for PostgreSQL, etc.) rather than running the in-cluster StatefulSet. This is the expected production path: managed services give you automated backups, point-in-time recovery, HA, and patch management out of the box. Flip `externalPostgres.enabled: true` in your values and pre-create the password Secret as described in the chart README's [External Postgres](../deploy/k8s/charts/subscription-service/README.md#external-postgres-ticket-416) section. The chart will skip its own Postgres StatefulSet/Service/Secret and wire HAPI to the host you specify.

---

## Troubleshooting

### `Init:CrashLoopBackOff` on hapi / matchbox

The `fetch-igs` init container failed. Inspect:

```bash
kubectl -n <ns> logs <pod> -c fetch-igs
```

Common causes:

| Symptom | Fix |
|---------|-----|
| `curl: (60) SSL certificate problem` | TLS-inspecting proxy in the path. Set `hapi.igFetcherInsecure: true` and `matchbox.igFetcherInsecure: true` (the `values-rancher.yaml` overlay already does this for local laptops). |
| `curl: (6) Could not resolve host: packages.fhir.org` | No DNS/internet egress from the pod network. Configure cluster DNS or mirror the packages to an internal registry and override `igRegistry`. |
| `curl: (22) HTTP 404` | Bad package name or version. Verify `<igRegistry>/<name>/<version>` returns a tarball in a browser. |

### `ImagePullBackOff` on hapi / interface-engine

The locally-built image is missing from the cluster's image store. In **dockerd-mode** Rancher Desktop, just `docker build`. In **containerd-mode**, do the `docker save | nerdctl --namespace k8s.io load` dance documented above.

### HAPI pod takes 90+ seconds to become Ready

Normal. The IG install on a fresh database takes a while. The chart's readiness probe has `initialDelaySeconds=60`, `periodSeconds=10`, `failureThreshold=30` (so HAPI has 60 + 30*10 = 360 seconds of grace before the pod is marked unhealthy).

### `helm upgrade` leaves old ReplicaSets running

If `helm upgrade` changes immutable fields (e.g. emptyDir vs configMap volume), the Deployment will keep an old broken ReplicaSet around alongside the new one. Drop the old ones:

```bash
kubectl -n <ns> get rs
kubectl -n <ns> delete rs <name-of-old-rs>
```

A normal rolling update (image bump, env-var change) handles this automatically.

### LoadBalancer Service has no EXTERNAL-IP

On a real cloud cluster, that's the cloud-provider LB controller hasn't picked it up yet — wait 1-2 minutes. On Rancher Desktop, klipper-lb gives every LoadBalancer the node's external IP (`192.168.64.2` or similar) and also binds the port on the host (so `localhost:2575` works).

---

## Acceptance evidence (#363 + #364)

| Check | Result |
|-------|--------|
| `helm template deploy/k8s/charts/subscription-service` | 13 manifests render |
| `helm template ... -f values-rancher.yaml` | 13 manifests render |
| `helm template ... -f values-dev.yaml` | 13 manifests render |
| `helm template ... \| kubectl apply --dry-run=client -f -` | All 14 objects valid |
| `helm lint deploy/k8s/charts/subscription-service` | 0 errors, 0 warnings |
| `helm install subsvc -n subsvc-test ... values-rancher.yaml` | All 4 pods Ready |
| `GET /fhir/metadata` via traefik ingress | HTTP 200, CapabilityStatement, FHIR 4.0.1, 146 resource types |
| `POST /fhir/Patient`, `GET /fhir/Patient/<id>` | HTTP 201 then HTTP 200, round-trip OK |
| ADT^A04 over MLLP `nc localhost 2575` | AA ACK returned |
| `helm uninstall && kubectl delete ns` | Clean teardown |
