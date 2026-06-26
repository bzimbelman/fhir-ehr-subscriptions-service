# Deployment recipes

Concrete recipes for running subscription-service in different environments. The compose stack and the Helm chart both expose HAPI on an HTTP port; this directory collects working recipes for the surrounding pieces (reverse proxy, ingress, cluster-specific quirks, image registries).

## By cluster / platform

| Recipe | When to use |
|---|---|
| [AWS EKS](k8s-eks.md) | EKS or eks-anywhere; ALB / NLB; ECR; RDS |
| [Google GKE](k8s-gke.md) | GKE Standard or Autopilot; GCE LB / Cloud LB; GAR; Cloud SQL |
| [Azure AKS](k8s-aks.md) | AKS; Azure LB / Application Gateway; ACR; Azure DB for PostgreSQL |
| [Kubernetes Ingress (generic)](k8s-ingress.md) | Other k8s clusters with an IngressClass (OpenShift, on-prem with ingress-nginx, etc.) |

## By reverse proxy / tunnel (Docker Compose deployments)

| Recipe | When to use |
|---|---|
| [Cloudflare tunnel](cloudflare-tunnel.md) | Cloudflare account + domain; HTTPS without opening firewall ports |
| [Caddy reverse proxy](caddy-reverse-proxy.md) | Automatic Let's Encrypt TLS with one config file; VPS with a public IP |
| [Traefik](traefik.md) | Already running Traefik for other services |
| [nginx](nginx.md) | Classic reverse proxy; full manual control |
| [Direct port-forward](direct-port-forward.md) | Local dev only; no proxy in front |

## Image registry

[`image-registry.md`](image-registry.md) covers the workflow common to all OCI registries (Docker Hub, ECR, GCR/GAR, ACR, Harbor, etc.) — building, tagging, pushing, image-pull secrets, image signing. The cloud-specific recipes layer cloud-CLI helpers on top.

## All recipes assume

- The compose stack or Helm release is already running
- HAPI is reachable internally (`/fhir/metadata` returns a CapabilityStatement on the chosen internal port)

## MLLP isn't covered here

These recipes are for the **FHIR HTTP API**. MLLP is plain TCP and most HTTP-only proxies can't carry it. MLLP ingress is LAN/VPN-only by design in the first version of the system — see [`../architecture.md`](../architecture.md) "HL7 MLLP ingress".

## Reference deployment

The maintainer's reference instance runs on a single Docker host behind a Cloudflare tunnel, configured per [`cloudflare-tunnel.md`](cloudflare-tunnel.md). That's *one* specific deployment; nothing about the project requires that shape.
