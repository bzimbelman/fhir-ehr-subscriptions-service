# Cloudflare tunnel

Use this when you have:

- A domain on Cloudflare (DNS managed by Cloudflare)
- A Cloudflare account (free tier works)
- A subscription-service deployment somewhere — could be your laptop, a VPS, a home server, anywhere with internet
- Need HTTPS access from outside the deployment's network *without* opening firewall ports

The tunnel is an outbound HTTPS connection from the host to Cloudflare; you never expose an inbound port. Cloudflare terminates TLS and routes requests over the tunnel to your local HAPI.

## One-time setup

```bash
# Install cloudflared (macOS, Linux, etc.)
# https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/install-and-setup/installation/

# Authenticate with your Cloudflare account (opens a browser)
cloudflared tunnel login

# Create a named tunnel (one per deployment is fine)
cloudflared tunnel create subscription-service
# Note the tunnel UUID it prints (32 hex digits in 8-4-4-4-12 form, e.g. <your-tunnel-uuid>)

# Create the DNS CNAME pointing your hostname at the tunnel
cloudflared tunnel route dns subscription-service fhir.your-domain.com
```

## Tunnel config

Create `~/.cloudflared/config.yml` (or wherever you keep cloudflared configs):

```yaml
tunnel: <tunnel-uuid>
credentials-file: /home/you/.cloudflared/<tunnel-uuid>.json
no-autoupdate: true

ingress:
  - hostname: fhir.your-domain.com
    service: http://localhost:18080
  # Catch-all (required by cloudflared)
  - service: http_status:404
```

If you're already running cloudflared with other ingress rules (a tunnel can serve multiple hostnames), add the `fhir.your-domain.com` entry **before** the catch-all, not after.

## Run the tunnel

```bash
cloudflared tunnel --config ~/.cloudflared/config.yml run
```

For production, run cloudflared as a systemd service (Linux) or a launchd agent (macOS) so it restarts on boot.

## Verify

```bash
curl -fsS https://fhir.your-domain.com/fhir/metadata | jq '.software'
# {"name": "HAPI FHIR Server", "version": "7.6.0"}
```

## Updating the tunnel config

`cloudflared` doesn't reliably reload on `SIGHUP` in all versions. The safest pattern: stop the cloudflared process and start it again. If you're running multiple cloudflared instances (e.g., a primary + a backup), stop and restart each in turn — leaving the old config running on one instance while the new config runs on another causes confusing 404s from whichever instance Cloudflare routes a request to.

## What you don't get

- **MLLP**: Cloudflare's free tunnel is HTTP/HTTPS/WebSocket only. MLLP is plain TCP and won't carry. Use Cloudflare Spectrum (paid) for TCP, OR keep MLLP on the LAN/VPN.
- **Custom TLS certs**: Cloudflare terminates with its own certs. The tunnel between Cloudflare and your host is also TLS but uses Cloudflare's CA.

## Auth

The tunnel doesn't authenticate requests; that's HAPI's job (via the OIDC interceptor — see [`../auth.md`](../auth.md)). Optionally, layer Cloudflare Access on top to require an additional identity check at the edge before any request even reaches HAPI.
