# Azure AKS deployment

Recipe for deploying subscription-service to an Azure Kubernetes Service cluster.

For the registry and image-build workflow, see [image-registry.md](image-registry.md).

## Prerequisites

- AKS 1.28+ cluster
- `az` CLI configured, `kubectl` pointing at the cluster
- An Azure Container Registry (ACR):
  ```bash
  az acr create --resource-group <rg> --name <acrname> --sku Standard
  az aks update --resource-group <rg> --name <aksname> --attach-acr <acrname>
  # AKS-attached ACR: nodes pull without imagePullSecrets
  ```
- One of:
  - [AGIC](https://learn.microsoft.com/en-us/azure/application-gateway/ingress-controller-overview) (Application Gateway Ingress Controller) — Azure-native, but more setup
  - [ingress-nginx](https://kubernetes.github.io/ingress-nginx/) — community standard, simpler
- (Recommended) cert-manager installed with an ACME ClusterIssuer

## Push images to ACR

```bash
ACR=<acrname>.azurecr.io
az acr login --name <acrname>

TAG=$(git rev-parse --short HEAD)

docker build -t ${ACR}/subscription-service-hapi:${TAG}             hapi/
docker build -t ${ACR}/subscription-service-interface-engine:${TAG} interface-engine/

docker push ${ACR}/subscription-service-hapi:${TAG}
docker push ${ACR}/subscription-service-interface-engine:${TAG}
```

If you didn't run `az aks update --attach-acr` and don't want to, create an image-pull secret instead:

```bash
SP_PASSWORD=$(az ad sp create-for-rbac --name acr-pull-sp --scopes $(az acr show --name <acrname> --query id -o tsv) --role acrpull --query password -o tsv)
SP_ID=$(az ad sp list --display-name acr-pull-sp --query "[0].appId" -o tsv)
kubectl create secret docker-registry acr-pull \
  --docker-server=${ACR} --docker-username=${SP_ID} --docker-password=${SP_PASSWORD} \
  -n subscription-service
```

## values-aks.yaml

```yaml
# values-aks.yaml
image:
  hapi:
    repository: <acrname>.azurecr.io/subscription-service-hapi
    tag: "<git-sha>"
    pullPolicy: Always
  interfaceEngine:
    repository: <acrname>.azurecr.io/subscription-service-interface-engine
    tag: "<git-sha>"
    pullPolicy: Always

# AKS-attached ACR: no imagePullSecrets needed
imagePullSecrets: []
# OR with the service-principal pull secret:
# imagePullSecrets:
#   - name: acr-pull

postgres:
  storage:
    storageClassName: managed-csi    # default Azure Disk CSI; "managed-csi-premium" for SSD
    size: 50Gi

# OR use Azure DB for PostgreSQL Flexible Server (recommended for prod):
# externalPostgres:
#   enabled: true
#   host: subsvc-prod.postgres.database.azure.com
#   passwordSecret: azure-db-pw
#   sslMode: require                 # Azure DB requires TLS; verify-full needs CA bundle

ingress:
  enabled: true
  className: nginx                   # if using ingress-nginx
  # OR: className: azure-application-gateway (for AGIC)
  annotations:
    # ingress-nginx with cert-manager:
    cert-manager.io/cluster-issuer: letsencrypt-prod
  hosts:
    - host: subscription-service.example.com
      paths:
        - path: /
          pathType: Prefix
  certManager:
    enabled: true
    clusterIssuer: letsencrypt-prod

# MLLP via Azure Load Balancer
interfaceEngine:
  mllp:
    service:
      type: LoadBalancer
      port: 2575
      annotations:
        # Internal-only LB for MLLP (most facilities want VPN-attached EHRs):
        service.beta.kubernetes.io/azure-load-balancer-internal: "true"
        # Pin to a specific subnet if needed:
        # service.beta.kubernetes.io/azure-load-balancer-internal-subnet: <subnet>

# PDBs on for production
podDisruptionBudgets:
  enabled: true

monitoring:
  enabled: true
  serviceMonitor:
    labels:
      release: prometheus

# NetworkPolicy — AKS supports it natively with Calico or Azure CNI Overlay
networkPolicy:
  enabled: true

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

## Install

```bash
helm install subsvc deploy/k8s/charts/subscription-service \
  -n subscription-service --create-namespace \
  -f values-aks.yaml

kubectl -n subscription-service rollout status deployment/subsvc-hapi --timeout=15m

kubectl -n subscription-service get ingress
# subsvc-hapi  nginx  subscription-service.example.com  <public IP>  80, 443
```

Point your DNS A record at the public IP. cert-manager will provision the cert via ACME automatically.

## Azure DB for PostgreSQL as the Postgres

Recommended production path on AKS. Use the Flexible Server tier (Single Server is deprecated).

1. Create the server:
   ```bash
   az postgres flexible-server create \
     --resource-group <rg> \
     --name subsvc-prod \
     --location eastus \
     --tier GeneralPurpose --sku-name Standard_D2s_v3 \
     --version 16 \
     --storage-size 50 \
     --admin-user hapi \
     --admin-password "$(openssl rand -hex 24)" \
     --vnet <vnet> --subnet <subnet> \
     --backup-retention 14 \
     --high-availability ZoneRedundant
   ```
2. Create the database: `az postgres flexible-server db create --resource-group <rg> --server-name subsvc-prod --database-name hapi`
3. Configure private DNS or VNet integration so the AKS cluster can reach `subsvc-prod.postgres.database.azure.com`.
4. `kubectl create secret generic azure-db-pw --from-literal=password='<admin-password>' -n subscription-service`
5. Set `externalPostgres` block in values-aks.yaml (see above).
6. `helm upgrade subsvc ... -f values-aks.yaml`

## Workload Identity (for pods that need Azure API access)

If pods need to call Azure APIs (e.g., Azure Key Vault for secret rotation later), use [Azure AD Workload Identity](https://azure.github.io/azure-workload-identity/docs/) to federate the Kubernetes service account with an Azure AD app registration. This avoids storing service-principal credentials in pods. Out of scope for the base chart; layer on as needed.

## Smoke test

Same as the [EKS smoke test](k8s-eks.md#smoke-test) — replace the hostname and the MLLP LB.

## Common gotchas

- **`ImagePullBackOff` on ACR** — either the AKS-ACR attachment is missing, or the image tag doesn't exist. `az acr repository show-tags --name <acrname> --repository subscription-service-hapi` lists the tags actually in ACR.
- **AGIC vs ingress-nginx** — AGIC is more Azure-integrated (uses Application Gateway, supports WAF) but requires the AKS cluster to be deployed in a specific way (separate subnet for App Gateway, peering, etc.). ingress-nginx is simpler and what most teams pick unless they need WAF.
- **Azure DB connection error** — Azure DB Flexible Server is private-by-default; the AKS cluster must be in the same VNet or have peering set up. Use `nslookup` from a debug pod to verify DNS resolution before pointing HAPI at it.
- **cert-manager challenges failing** — Let's Encrypt HTTP-01 challenges need the ingress controller to be public. If you're using AGIC with WAF, the WAF rules can block the challenge path; whitelist `/.well-known/acme-challenge/*`.
