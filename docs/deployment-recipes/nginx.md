# nginx reverse proxy

Use this when you want full manual control over the TLS-terminating reverse proxy in front of HAPI, or you already run nginx for other services.

Assumes:

- subscription-service compose stack is running and HAPI is on `http://localhost:18080`
- You have an SSL cert + key for `fhir.your-domain.com` (Let's Encrypt via certbot is easiest)
- nginx is installed (`apt install nginx`, `brew install nginx`, etc.)

## Config

`/etc/nginx/sites-available/fhir.your-domain.com`:

```nginx
server {
    listen 80;
    server_name fhir.your-domain.com;
    # Redirect all HTTP -> HTTPS
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl http2;
    server_name fhir.your-domain.com;

    ssl_certificate     /etc/letsencrypt/live/fhir.your-domain.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/fhir.your-domain.com/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_prefer_server_ciphers on;

    # FHIR resources can be large (bundles, big binaries); raise the cap
    client_max_body_size 50M;

    location / {
        proxy_pass http://127.0.0.1:18080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # WebSocket support (for Subscription channel.type=websocket)
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";

        # FHIR operations can take a while; raise the timeout
        proxy_read_timeout 120s;
    }
}
```

Enable the site and reload:

```bash
sudo ln -s /etc/nginx/sites-available/fhir.your-domain.com /etc/nginx/sites-enabled/
sudo nginx -t      # test config
sudo systemctl reload nginx
```

## Let's Encrypt with certbot

```bash
sudo apt install certbot python3-certbot-nginx
sudo certbot --nginx -d fhir.your-domain.com
```

Certbot auto-renews via a systemd timer. Confirm with `sudo systemctl list-timers | grep certbot`.

## Verify

```bash
curl -fsS https://fhir.your-domain.com/fhir/metadata | jq '.software'
```

## What you don't get

- **MLLP**: nginx is HTTP-only by default. The `stream` module can proxy TCP but requires building nginx with the right modules and a separate config — not covered here.

## Auth

nginx doesn't authenticate FHIR requests. HAPI does (via the OIDC interceptor — see [`../auth.md`](../auth.md)). You can optionally add nginx-level auth (basic auth, ngx_http_auth_request, mTLS) as a defense-in-depth layer, but it's not required.
