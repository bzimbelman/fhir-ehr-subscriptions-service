# Kubernetes (Helm) deployment

Helm chart for deploying subscription-service to a Kubernetes cluster (Rancher Desktop, our own cloud cluster, a facility's cluster).

The chart lives at [`charts/subscription-service/`](charts/subscription-service/) — see [its README](charts/subscription-service/README.md) for the per-value reference. See [`docs/k8s-deployment.md`](../../docs/k8s-deployment.md) for the operator workflow (Rancher Desktop, dev/prod, troubleshooting).

## Layout

```
deploy/k8s/
└── charts/
    └── subscription-service/
        ├── Chart.yaml
        ├── values.yaml                 # defaults
        ├── values-dev.yaml             # bzonfhir.com dev cluster overrides
        ├── values-rancher.yaml         # local Rancher Desktop validation
        ├── README.md
        └── templates/
            ├── _helpers.tpl
            ├── configmap-hapi.yaml          # /app/config/application.yaml
            ├── configmap-healthcheck.yaml   # /app/healthcheck/HapiHealthCheck.java
            ├── secret-postgres.yaml         # DB user/password/dbname
            ├── secret-auth.yaml             # JWT issuer + JWKS URL
            ├── statefulset-postgres.yaml
            ├── service-postgres.yaml
            ├── deployment-hapi.yaml         # initContainer fetches IGs into emptyDir
            ├── service-hapi.yaml
            ├── deployment-matchbox.yaml     # initContainer fetches v2-to-FHIR IG
            ├── service-matchbox.yaml
            ├── deployment-ipf.yaml
            ├── service-ipf.yaml             # ClusterIP HTTP + LoadBalancer MLLP
            ├── ingress.yaml                 # FHIR HTTPS via traefik
            └── networkpolicy.yaml           # OPTIONAL; off by default
```

Same images as the Docker Compose target. Per-environment overrides via `values-*.yaml`.

## Quick start (Rancher Desktop)

See [the chart's README](charts/subscription-service/README.md#quick-start-rancher-desktop) and [`docs/k8s-deployment.md`](../../docs/k8s-deployment.md).
