# AWS EKS deployment

Recipe for deploying subscription-service to an Amazon EKS cluster. Assumes you have an EKS cluster, `aws` CLI configured, `kubectl` pointing at the cluster, and the [AWS Load Balancer Controller](https://kubernetes-sigs.github.io/aws-load-balancer-controller/) installed.

For the registry and image-build workflow, see [image-registry.md](image-registry.md).

## Prerequisites

- EKS 1.28+ cluster
- AWS Load Balancer Controller installed (for ALB-backed Ingress and NLB-backed LoadBalancer Services)
- EBS CSI driver installed (default in modern EKS) — provides the `gp3` StorageClass
- (Recommended) cert-manager installed with an ACME ClusterIssuer
- ECR repositories created for the two custom images:
  ```bash
  aws ecr create-repository --repository-name subscription-service-hapi
  aws ecr create-repository --repository-name subscription-service-interface-engine
  ```

## Push images to ECR

```bash
ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
REGION=$(aws configure get region)
REGISTRY=${ACCOUNT_ID}.dkr.ecr.${REGION}.amazonaws.com

aws ecr get-login-password --region ${REGION} \
  | docker login --username AWS --password-stdin ${REGISTRY}

TAG=$(git rev-parse --short HEAD)

docker build -t ${REGISTRY}/subscription-service-hapi:${TAG}             hapi/
docker build -t ${REGISTRY}/subscription-service-interface-engine:${TAG} interface-engine/

docker push ${REGISTRY}/subscription-service-hapi:${TAG}
docker push ${REGISTRY}/subscription-service-interface-engine:${TAG}
```

## values-eks.yaml

```yaml
# values-eks.yaml
image:
  hapi:
    repository: ACCOUNT.dkr.ecr.REGION.amazonaws.com/subscription-service-hapi
    tag: "<git-sha>"
    pullPolicy: Always
  interfaceEngine:
    repository: ACCOUNT.dkr.ecr.REGION.amazonaws.com/subscription-service-interface-engine
    tag: "<git-sha>"
    pullPolicy: Always

# EKS nodes have IAM roles for ECR pull via the kubelet — no imagePullSecrets needed
# IF the node IAM role has AmazonEC2ContainerRegistryReadOnly attached.
imagePullSecrets: []

postgres:
  storage:
    storageClassName: gp3            # SSD-backed; modern EKS default
    size: 50Gi

# OR (recommended) use RDS:
# externalPostgres:
#   enabled: true
#   host: subsvc-prod.abc123.us-east-1.rds.amazonaws.com
#   passwordSecret: rds-pw           # pre-create from AWS Secrets Manager
#   sslMode: verify-full

ingress:
  enabled: true
  className: alb                     # AWS Load Balancer Controller
  annotations:
    alb.ingress.kubernetes.io/scheme: internet-facing
    alb.ingress.kubernetes.io/target-type: ip
    alb.ingress.kubernetes.io/listen-ports: '[{"HTTP": 80}, {"HTTPS": 443}]'
    alb.ingress.kubernetes.io/ssl-redirect: '443'
    # If using ACM cert (preferred for ALB):
    alb.ingress.kubernetes.io/certificate-arn: arn:aws:acm:us-east-1:ACCOUNT:certificate/...
    # If using cert-manager + Let's Encrypt instead, omit the ACM line
    # and set ingress.certManager.enabled: true below.
  hosts:
    - host: subscription-service.example.com
      paths:
        - path: /
          pathType: Prefix
  # When using ACM, don't set tls (ALB terminates with the ACM cert directly).
  # When using cert-manager, set tls + certManager.
  tls: []
  certManager:
    enabled: false
    # clusterIssuer: letsencrypt-prod

# MLLP via Network Load Balancer (TCP). NLB is the right choice; ALB is HTTP only.
interfaceEngine:
  mllp:
    service:
      type: LoadBalancer
      port: 2575
      annotations:
        service.beta.kubernetes.io/aws-load-balancer-type: external
        service.beta.kubernetes.io/aws-load-balancer-nlb-target-type: ip
        service.beta.kubernetes.io/aws-load-balancer-scheme: internet-facing
        # For internal-only MLLP (VPN-attached EHRs), use scheme: internal instead

# PDBs on for production
podDisruptionBudgets:
  enabled: true

# ServiceMonitors for Prometheus, if you run Prometheus Operator on the cluster
monitoring:
  enabled: true
  serviceMonitor:
    labels:
      release: prometheus            # match your Prometheus's serviceMonitorSelector

# NetworkPolicy on for production (requires a NetworkPolicy controller — EKS uses VPC CNI; turn on the CNI's NetworkPolicy support, or install Cilium/Calico)
networkPolicy:
  enabled: true

# OIDC auth — production: ON
featureToggles:
  auth:
    enabled: true
    issuer: https://your-keycloak.example.com/realms/subscription-service
    # jwksUrl autocomputed
  validation:
    mode: warn
  channelSecurity:
    mode: strict
  multitenancy:
    mode: disabled                   # or enabled if multi-customer
```

## Install

```bash
helm install subsvc deploy/k8s/charts/subscription-service \
  -n subscription-service --create-namespace \
  -f values-eks.yaml

# Wait for rollout
kubectl -n subscription-service rollout status statefulset/subsvc-postgres
kubectl -n subscription-service rollout status deployment/subsvc-hapi --timeout=10m
kubectl -n subscription-service rollout status deployment/subsvc-matchbox
kubectl -n subscription-service rollout status deployment/subsvc-interface-engine

# Verify the ALB is provisioned
kubectl -n subscription-service get ingress
# NAME          CLASS   HOSTS                              ADDRESS                                  PORTS
# subsvc-hapi   alb     subscription-service.example.com   k8s-...-...elb.amazonaws.com             80, 443
```

Point your DNS at the ALB hostname (Route 53 alias record, or A/CNAME on whatever DNS you use).

## RDS as the Postgres

Recommended production path. RDS gives you automated backups, point-in-time recovery, multi-AZ HA, encryption at rest, and patch management.

1. Create the RDS instance:
   ```bash
   aws rds create-db-instance \
     --db-instance-identifier subsvc-prod \
     --db-instance-class db.t3.medium \
     --engine postgres --engine-version 16.4 \
     --allocated-storage 50 \
     --storage-type gp3 --storage-encrypted \
     --master-username hapi \
     --master-user-password "$(openssl rand -hex 24)" \
     --backup-retention-period 14 \
     --multi-az
   ```
2. Set the security group to allow inbound from the EKS node security group on port 5432.
3. Create the database:
   ```bash
   psql -h subsvc-prod.../...rds.amazonaws.com -U hapi -d postgres -c "CREATE DATABASE hapi;"
   ```
4. Create the Kubernetes Secret with the password:
   ```bash
   kubectl create secret generic rds-pw \
     --from-literal=password='<the-master-password>' \
     -n subscription-service
   ```
5. Set in `values-eks.yaml`:
   ```yaml
   externalPostgres:
     enabled: true
     host: subsvc-prod.abc123.us-east-1.rds.amazonaws.com
     port: 5432
     database: hapi
     user: hapi
     passwordSecret: rds-pw
     sslMode: verify-full
   ```
6. Reinstall: `helm upgrade subsvc ... -f values-eks.yaml`

Disable in-cluster Postgres by setting `externalPostgres.enabled: true` — the chart automatically skips the StatefulSet, Service, and password Secret.

## Smoke test

```bash
HOSTNAME=subscription-service.example.com

# 1. CapabilityStatement
curl -fsS https://${HOSTNAME}/fhir/metadata | jq '{fhirVersion, software:.software.name}'

# 2. Auth check (expect 401)
curl -i https://${HOSTNAME}/fhir/Patient

# 3. With a token (replace <token> with one from Keycloak)
curl -fsS -H "Authorization: Bearer <token>" https://${HOSTNAME}/fhir/Patient/$count

# 4. MLLP over NLB
MLLP_LB=$(kubectl -n subscription-service get svc subsvc-interface-engine-mllp -o jsonpath='{.status.loadBalancer.ingress[0].hostname}')
{ printf '\x0b'; printf 'MSH|^~\\&|TEST|FAC|HAPI|HOSP|20260626120000||ADT^A04|MSG00001|P|2.5\rEVN||20260626120000\rPID|1||M1^^^HOSP^MR||Smoke^Test||19800101|M\rPV1|1|O\r'; printf '\x1c\r'; } \
  | nc -w 5 ${MLLP_LB} 2575 | xxd | head -3
# Expect "MSA|AA|MSG00001"
```

## Cost considerations

A minimal production EKS deploy:

- 1× t3.medium EKS managed node group: ~$30/mo
- ALB: ~$22/mo + traffic
- NLB (for MLLP): ~$22/mo + traffic
- RDS db.t3.medium Multi-AZ: ~$140/mo + storage
- 50GB gp3 EBS PVC (in-cluster Postgres path): ~$5/mo

You can save money by using a smaller node group, dropping multi-AZ on the RDS, or using a NetworkLoadBalancer for the HTTP ingress instead of an ALB (cheaper but no path-based routing).

## Common gotchas

- **`Pending` PVC** — the EBS CSI driver isn't installed or the `gp3` StorageClass doesn't exist. Verify with `kubectl get storageclass`.
- **ALB doesn't get a public IP** — the AWS Load Balancer Controller isn't installed or its IAM role doesn't have the right permissions. Check the controller's logs.
- **Pods can't pull from ECR** — the EKS node IAM role is missing `AmazonEC2ContainerRegistryReadOnly`. Either attach it, or use `imagePullSecrets` with explicit credentials.
- **HAPI takes >5min to start** — normal on first install (IG download). If it exceeds 10 minutes, check the `fetch-igs` init container logs for DNS / egress issues.
- **PSA `restricted` violations on Autopilot or hardened clusters** — the chart's PSA defaults satisfy `restricted`; if you see violations, check that you haven't accidentally overridden `podSecurityContext` / `securityContext` in your values.
