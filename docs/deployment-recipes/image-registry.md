# Image registry workflow

Any OCI-compatible registry works for the subscription-service images. This doc covers the workflow common to all registries; the cloud-specific recipes (EKS / GKE / AKS) cover provider-specific details on top.

## Which images you push

Three images are involved:

| Image | Source | Push required? |
|---|---|---|
| `subscription-service/hapi` | Built locally from `hapi/Dockerfile` (derived from `hapiproject/hapi:v7.6.0` with our auth/validation/channel-security/multi-tenancy JAR layered in) | **Yes** — this is our derived image |
| `subscription-service/interface-engine` | Built locally from `interface-engine/Dockerfile` | **Yes** — this is our app |
| `postgres:16-alpine`, `europe-west6-docker.pkg.dev/ahdis-ch/ahdis/matchbox:v3.9.13`, `curlimages/curl:8.10.1` | Upstream public images | No — pulled directly from their public registries unless you're air-gapped or want to mirror for supply-chain control |

For an air-gapped cluster, mirror all five images to your internal registry and override every `image.*.repository` value.

## Picking a registry

Public:

- **Docker Hub** — free for public repos, paid for private. Rate-limited for anonymous pulls. Good for OSS projects, less common for enterprise.

Private / cloud-hosted (covered in the cloud recipes):

- **AWS ECR** — see [k8s-eks.md](k8s-eks.md)
- **Google Artifact Registry / GCR** — see [k8s-gke.md](k8s-gke.md)
- **Azure Container Registry** — see [k8s-aks.md](k8s-aks.md)

Private / self-hosted:

- **Harbor** — open source, full-featured, runs in-cluster or alongside it
- **Gitea / Forgejo registry** — lightweight, integrates with your Gitea git server
- **JFrog Artifactory**, **Nexus** — enterprise-grade with their own deployment models

This doc treats them all the same. The only thing that changes between registries is the hostname and the auth recipe.

## Build the images locally

From the repo root:

```bash
docker build -t your-registry.example.com/subscription-service-hapi:<tag>             hapi/
docker build -t your-registry.example.com/subscription-service-interface-engine:<tag> interface-engine/
```

Pick `<tag>` based on your release strategy:

- Git SHA (recommended for traceability): `<tag>=$(git rev-parse --short HEAD)`
- Version + git SHA: `<tag>=v0.3.0-a1b2c3d4`
- Branch name: `<tag>=main` (least specific; OK for dev environments)

Avoid `latest` — it can't be reliably pinned by a Helm chart.

## Authenticate

### Docker Hub

```bash
docker login
# Username: <your-user>
# Password: <a personal access token, NOT your account password>
```

### Cloud registries

Use the cloud's CLI helper rather than `docker login`. Each cloud has its own command — see the cloud-specific recipe.

### Private registry with username + password

```bash
docker login your-registry.example.com -u <user> -p <password>
```

For CI, set `--password-stdin` instead:

```bash
echo "$REGISTRY_PASSWORD" | docker login your-registry.example.com -u <user> --password-stdin
```

## Push

```bash
docker push your-registry.example.com/subscription-service-hapi:<tag>
docker push your-registry.example.com/subscription-service-interface-engine:<tag>
```

Verify with the registry's UI or CLI (`docker pull` from a different machine works as a smoke test).

## Tell the chart where to pull from

In your `values-<env>.yaml`:

```yaml
image:
  hapi:
    repository: your-registry.example.com/subscription-service-hapi
    tag: "<tag>"
    pullPolicy: Always           # Always for tagged releases; IfNotPresent for local/dev
  interfaceEngine:
    repository: your-registry.example.com/subscription-service-interface-engine
    tag: "<tag>"
    pullPolicy: Always
```

`Always` is safer for production: even if the tag doesn't change, the kubelet will re-pull and pick up any registry-side updates (rare but possible with mutable tags). `IfNotPresent` is the right pick for immutable tags or local-build workflows where you don't want a pull attempt to fail when the cluster can't reach the registry.

## Image-pull credentials (private registries only)

If your registry requires authentication, create a Kubernetes Secret of type `docker-registry` in the namespace where you install the chart:

```bash
kubectl create secret docker-registry your-registry-cred \
  --docker-server=your-registry.example.com \
  --docker-username=<user> \
  --docker-password=<password> \
  --docker-email=<email> \
  -n <namespace>
```

Then reference it in values:

```yaml
imagePullSecrets:
  - name: your-registry-cred
```

For cloud registries, the auth helper usually creates this secret automatically. See the cloud-specific recipes.

For GitOps workflows, store the registry credentials in a sealed-secrets / external-secrets store and let the controller create the Secret in-cluster — never commit `--docker-password=<actual-password>` to git.

## Image scanning + signing (optional, recommended for production)

Two practices worth adopting on any registry:

- **Vulnerability scanning** with [Trivy](https://github.com/aquasecurity/trivy):
  ```bash
  trivy image your-registry.example.com/subscription-service-hapi:<tag>
  ```
  Most cloud registries (ECR, GCR/GAR, ACR) offer this built-in; turn it on.
- **Signing** with [Cosign](https://github.com/sigstore/cosign):
  ```bash
  cosign sign --key cosign.key your-registry.example.com/subscription-service-hapi:<tag>
  ```
  Pair with a Kyverno / OPA Gatekeeper policy that refuses to admit unsigned images.

Both are out of scope for the chart itself — they're cluster-level policy decisions — but the chart's standard OCI artifacts work with both tools out of the box.

## See also

- [k8s-deployment.md](../k8s-deployment.md) — the operator workflow that consumes these images
- [k8s-eks.md](k8s-eks.md), [k8s-gke.md](k8s-gke.md), [k8s-aks.md](k8s-aks.md) — cloud-specific registry recipes
