# Deployment recipes

The subscription-service compose stack and Helm chart both put HAPI on an HTTP port. Making that port reachable from outside the host (or outside the cluster) is a deployment-specific decision. This directory collects working recipes.

Pick the one that matches your environment:

| Recipe | When to use |
|---|---|
| [Cloudflare tunnel](cloudflare-tunnel.md) | You have a Cloudflare account, a domain on Cloudflare, and you want HTTPS to your service from anywhere without opening firewall ports |
| [Caddy reverse proxy](caddy-reverse-proxy.md) | You want automatic Let's Encrypt TLS with one config file; you're running on a VPS with a public IP |
| [Traefik](traefik.md) | You're already running Traefik for other services |
| [nginx](nginx.md) | Classic reverse proxy; you want full manual control |
| [Kubernetes Ingress](k8s-ingress.md) | You're deploying via Helm and your cluster has an IngressClass |
| [Direct port-forward](direct-port-forward.md) | Local dev only; no proxy in front |

All recipes assume the compose stack or Helm release is already running and HAPI is reachable internally (e.g., `http://localhost:18080/fhir/metadata` returns a CapabilityStatement).

## MLLP isn't covered here

The recipes are for the **FHIR HTTP API**. MLLP is plain TCP and most HTTP-only proxies can't carry it. MLLP ingress is LAN/VPN-only by design in the first version of the system — see [`../architecture.md`](../architecture.md) "HL7 MLLP ingress".

## Reference deployment

The maintainer's reference instance runs on a single Docker host behind a Cloudflare tunnel, configured per [`cloudflare-tunnel.md`](cloudflare-tunnel.md). That's *one* specific deployment; nothing about the project requires that shape.
