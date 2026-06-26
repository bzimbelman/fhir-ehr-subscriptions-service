# Google GKE deployment

Recipe for deploying subscription-service to a Google Kubernetes Engine cluster. Covers both **GKE Standard** and **GKE Autopilot** (which enforces PSA `restricted` by default — the chart is built for this).

For the registry and image-build workflow, see [image-registry.md](image-registry.md).

## Prerequisites

- GKE 1.28+ cluster (Standard or Autopilot)
- `gcloud` CLI configured, `kubectl` pointing at the cluster
- A Google Artifact Registry repository:
  ```bash
  gcloud artifacts repositories create subscription-service \
    --repository-format=docker \
    --location=us-central1
  ```
- (Recommended) cert-manager installed with an ACME ClusterIssuer, OR plan to use Google-managed certs

## Push images to GAR

```bash
gcloud auth configure-docker us-central1-docker.pkg.dev

PROJECT=$(gcloud config get-value project)
REGISTRY=us-central1-docker.pkg.dev/${PROJECT}/subscription-service

TAG=$(git rev-parse --short HEAD)

docker build -t ${REGISTRY}/hapi:${TAG}             hapi/
docker build -t ${REGISTRY}/interface-engine:${TAG} interface-engine/

docker push ${REGISTRY}/hapi:${TAG}
docker push ${REGISTRY}/interface-engine:${TAG}
```

## values-gke.yaml

```yaml
# values-gke.yaml
image:
  hapi:
    repository: us-central1-docker.pkg.dev/<project>/subscription-service/hapi
    tag: "<git-sha>"
    pullPolicy: Always
  interfaceEngine:
    repository: us-central1-docker.pkg.dev/<project>/subscription-service/interface-engine
    tag: "<git-sha>"
    pullPolicy: Always

# GKE node service accounts have GAR pull access if granted the
# `roles/artifactregistry.reader` role. No imagePullSecrets needed.
imagePullSecrets: []

postgres:
  storage:
    storageClassName: standard-rwo   # or "premium-rwo" for SSD
    size: 50Gi

# OR (recommended) use Cloud SQL:
# externalPostgres:
#   enabled: true
#   host: 10.x.x.x                   # Cloud SQL private IP (preferred over Cloud SQL Auth Proxy)
#   passwordSecret: cloud-sql-pw
#   sslMode: require                 # or verify-full with the Cloud SQL server CA

ingress:
  enabled: true
  className: gce                     # default GKE Ingress controller; switch to "nginx" if you run ingress-nginx
  annotations:
    # Google-managed cert (preferred on GKE):
    networking.gke.io/managed-certificates: subsvc-cert
    # Static IP (recommended for stable DNS)
    kubernetes.io/ingress.global-static-ip-name: subsvc-ip
  hosts:
    - host: subscription-service.example.com
      paths:
        - path: /
          pathType: Prefix
  tls: []
  certManager:
    enabled: false
    # Set to true if using cert-manager + Let's Encrypt instead of managed-certs

# MLLP via Cloud Load Balancer (TCP)
interfaceEngine:
  mllp:
    service:
      type: LoadBalancer
      port: 2575
      annotations:
        # Internal-only LB for MLLP (most facilities want VPN-attached EHRs, not public):
        cloud.google.com/load-balancer-type: Internal
        # Backend service config for TCP keepalive:
        cloud.google.com/neg: '{"exposed_ports":{"2575":{}}}'

# PDBs on for production
podDisruptionBudgets:
  enabled: true

monitoring:
  enabled: true
  serviceMonitor:
    labels:
      release: prometheus

networkPolicy:
  enabled: true                      # GKE supports NetworkPolicy natively (with Calico or Dataplane V2)

featureToggles:
  auth:
    enabled: true
    issuer: https://your-keycloak.example.com/realms/subscription-service
  validation:
    mode: warn
  channelSecurity:
    mode: strict
  multitenancy:
    mode: disabled
```

## Google-managed certificate (for the Ingress)

If using `networking.gke.io/managed-certificates` (preferred on GKE):

```yaml
# managed-cert.yaml
apiVersion: networking.gke.io/v1
kind: ManagedCertificate
metadata:
  name: subsvc-cert
  namespace: subscription-service
spec:
  domains:
    - subscription-service.example.com
```

```bash
kubectl apply -f managed-cert.yaml
```

The cert is provisioned by GCP and bound to the Ingress automatically. Takes 10-60 minutes to issue the first time.

## Install

```bash
# Reserve the static IP for the Ingress (recommended for stable DNS)
gcloud compute addresses create subsvc-ip --global

helm install subsvc deploy/k8s/charts/subscription-service \
  -n subscription-service --create-namespace \
  -f values-gke.yaml

kubectl -n subscription-service rollout status deployment/subsvc-hapi --timeout=15m

kubectl -n subscription-service get ingress
# subsvc-hapi  gce  subscription-service.example.com  <static IP>  80, 443
```

Point your DNS A record at the static IP. Wait for the managed cert to provision (`kubectl get managedcertificate subsvc-cert -n subscription-service -o yaml` → status should show `Active`).

## GKE Autopilot

Autopilot enforces PSA `restricted` and a few additional constraints:

- No `hostNetwork`, no `hostPath` volumes — the chart doesn't use either, so this is a non-issue
- Per-pod resource requests are mandatory — the chart already sets reasonable requests in `values.yaml`
- No DaemonSets in the kube-system namespace — n/a for us
- Smaller subset of supported StorageClasses — `standard-rwo` and `premium-rwo` both work

The chart's defaults work on Autopilot without modification. The only practical difference vs. Standard is that Autopilot bills by pod resources rather than node, so right-size your `resources.requests`.

## Cloud SQL as the Postgres

Recommended production path on GKE. Two connection patterns:

### Private IP (preferred)

1. Create the Cloud SQL instance with a private IP on the same VPC as the GKE cluster:
   ```bash
   gcloud sql instances create subsvc-prod \
     --database-version=POSTGRES_16 \
     --tier=db-custom-2-7680 \
     --region=us-central1 \
     --network=default \
     --no-assign-ip
   ```
2. Get the private IP: `gcloud sql instances describe subsvc-prod --format='value(ipAddresses[0].ipAddress)'`
3. Create database + user:
   ```bash
   gcloud sql databases create hapi --instance=subsvc-prod
   gcloud sql users create hapi --instance=subsvc-prod --password="$(openssl rand -hex 24)"
   ```
4. `kubectl create secret generic cloud-sql-pw --from-literal=password='<the-password>' -n subscription-service`
5. Set in values:
   ```yaml
   externalPostgres:
     enabled: true
     host: 10.x.x.x                  # private IP from step 2
     port: 5432
     database: hapi
     user: hapi
     passwordSecret: cloud-sql-pw
     sslMode: require
   ```

### Cloud SQL Auth Proxy (if private IP isn't an option)

Run the Cloud SQL Auth Proxy as a sidecar in the HAPI pod, connecting to `127.0.0.1:5432`. This is more complex and not covered in detail here — see [Google's docs](https://cloud.google.com/sql/docs/postgres/connect-auth-proxy).

## Smoke test

Same as the [EKS smoke test](k8s-eks.md#smoke-test) — replace the hostname and MLLP LB.

## Common gotchas

- **`MountVolume.SetUp failed` on Postgres** — the `gce-pd` CSI driver isn't installed. GKE installs it by default; if you're on a stripped-down cluster, install it explicitly.
- **Managed cert stays `Provisioning` forever** — DNS isn't pointing at the Ingress IP yet, or your domain is on a TLD GCP can't validate. Check `kubectl describe managedcertificate subsvc-cert`.
- **`gcr.io/...` pull errors on Autopilot** — the workload identity isn't bound to a service account with GAR/GCR read. Use the default `default` SA with the `artifactregistry.reader` role on the project.
- **Workload Identity for AWS access** — if you need GCP API access from inside a pod, bind a Google service account to the Kubernetes SA via Workload Identity. See Google's docs.
