# Kubernetes Ingress (generic)

Recipe for clusters that have an IngressClass but aren't one of EKS/GKE/AKS. Examples: OpenShift, on-prem with ingress-nginx, Rancher with Traefik, k3s, kind, RKE, etc.

If you're on one of the major clouds, the cloud-specific recipe ([EKS](k8s-eks.md) / [GKE](k8s-gke.md) / [AKS](k8s-aks.md)) covers more of the surrounding pieces (managed certs, cloud LB annotations, managed Postgres).

## Prerequisites

- Kubernetes 1.28+ cluster
- An IngressClass installed (ingress-nginx, Traefik, HAProxy, Contour, Kong, OpenShift Routes via the openshift-router shim, etc.)
- A StorageClass for the in-cluster Postgres PVC (any will do — `kubectl get storageclass`)
- Internet egress from pods (for the IG-fetch init containers — or mirror packages.fhir.org internally and override `hapi.igRegistry` / `matchbox.igRegistry`)

## Pick an IngressClass

`kubectl get ingressclass` — pick whichever matches your installed controller. Common values:

| Controller | `className` value |
|---|---|
| ingress-nginx | `nginx` |
| Traefik (Helm chart or Rancher Desktop) | `traefik` |
| Contour | `contour` |
| Kong | `kong` |
| HAProxy Ingress | `haproxy` |
| OpenShift router (via the [openshift-router-ingress-class shim](https://docs.openshift.com/container-platform/latest/networking/ingress-operator.html)) | `openshift-default` |

## values-k8s.yaml

```yaml
# values-k8s.yaml — generic Kubernetes
image:
  hapi:
    repository: <your-registry>/subscription-service-hapi
    tag: "<git-sha>"
    pullPolicy: Always
  interfaceEngine:
    repository: <your-registry>/subscription-service-interface-engine
    tag: "<git-sha>"
    pullPolicy: Always

imagePullSecrets:
  - name: your-registry-cred         # see ../image-registry.md

postgres:
  storage:
    storageClassName: ""             # "" uses the cluster default; set explicitly if you have multiple
    size: 50Gi

# OR external Postgres (recommended for production):
# externalPostgres:
#   enabled: true
#   host: postgres.your-internal-domain
#   passwordSecret: postgres-pw
#   sslMode: require

ingress:
  enabled: true
  className: nginx                   # or whatever your cluster has
  annotations:
    # Add controller-specific annotations here
    # ingress-nginx: nginx.ingress.kubernetes.io/proxy-body-size: "50m"  (for large FHIR bundles)
    # cert-manager: cert-manager.io/cluster-issuer: letsencrypt-prod
  hosts:
    - host: subscription-service.example.com
      paths:
        - path: /
          pathType: Prefix
  certManager:
    enabled: true
    clusterIssuer: letsencrypt-prod  # if you have cert-manager + a ClusterIssuer

interfaceEngine:
  mllp:
    service:
      type: LoadBalancer             # or NodePort / ClusterIP depending on how MLLP traffic reaches you
      port: 2575

podDisruptionBudgets:
  enabled: true

monitoring:
  enabled: true
  serviceMonitor:
    labels:
      release: prometheus

networkPolicy:
  enabled: true                      # requires a NetworkPolicy controller (Cilium, Calico, etc.)

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

## MLLP on non-cloud clusters

The MLLP `LoadBalancer` Service only gets an external IP if your cluster has a LoadBalancer-aware controller (MetalLB, kube-vip, klipper-lb on k3s, etc.). If you don't have one:

- **MetalLB**: install it, configure a pool of LAN IPs, then your `LoadBalancer` Services get IPs from the pool. Standard for on-prem.
- **NodePort fallback**: set `interfaceEngine.mllp.service.type: NodePort` in values. The MLLP listener becomes reachable on `<any-node-ip>:30000+` (k8s assigns a port; pin it with `nodePort: 32575` if needed). Less elegant; works without a LoadBalancer controller.
- **Reverse-TCP proxy**: run an HAProxy / nginx-stream / similar TCP proxy outside the cluster, forwarding `<external-ip>:2575` to one of the node IPs. The cluster-side `Service` can stay `ClusterIP`.

## OpenShift specifics

OpenShift's Routes are usually preferred over Ingress, but the chart's Ingress works with the openshift-default IngressClass (via the openshift-router shim). Two extras:

- **SCCs**: the chart's `restricted`-compatible security contexts work with the default `restricted-v2` SCC. If you've customized SCCs and require a specific UID range, override `podSecurityContext.runAsUser` per-workload.
- **Routes annotation alternative**: if you want to use Routes directly (for `passthrough` TLS or edge re-encrypt), disable the chart's Ingress (`ingress.enabled: false`) and add Route manifests separately.

## Smoke test

```bash
HOSTNAME=subscription-service.example.com

curl -fsS https://${HOSTNAME}/fhir/metadata | jq '{fhirVersion, software:.software.name}'

# MLLP — if LoadBalancer:
MLLP_IP=$(kubectl -n subscription-service get svc subsvc-interface-engine-mllp -o jsonpath='{.status.loadBalancer.ingress[0].ip}')

# MLLP — if NodePort:
# MLLP_IP=<any-node-ip>
# MLLP_PORT=$(kubectl -n subscription-service get svc subsvc-interface-engine-mllp -o jsonpath='{.spec.ports[0].nodePort}')

{ printf '\x0b'; printf 'MSH|^~\\&|TEST|FAC|HAPI|HOSP|20260626120000||ADT^A04|MSG00001|P|2.5\rEVN||20260626120000\rPID|1||M1^^^HOSP^MR||Smoke^Test||19800101|M\rPV1|1|O\r'; printf '\x1c\r'; } \
  | nc -w 5 ${MLLP_IP} 2575 | xxd | head -3
```

## Common gotchas

- **`Pending` PVC** — no default StorageClass. Either set `postgres.storage.storageClassName` explicitly or mark one of your StorageClasses default with `storageclass.kubernetes.io/is-default-class: "true"` annotation.
- **No external IP on MLLP Service** — no LoadBalancer controller. See "MLLP on non-cloud clusters" above.
- **`Forbidden: PodSecurity ... violates ... restricted` admission denials** — the chart's defaults satisfy `restricted`, but you may have overrides in your values. Check `kubectl describe pod` for the specific violation.
- **Ingress shows up but returns 404** — IngressClass mismatch. `kubectl get ingressclass`, then ensure `ingress.className` in values matches one of them.
