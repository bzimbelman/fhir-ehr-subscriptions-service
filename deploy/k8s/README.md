# Kubernetes (Helm) deployment

Helm chart for deploying subscription-service to a Kubernetes cluster (Rancher Desktop, our own cloud cluster, a facility's cluster).

## Layout (planned)

```
deploy/k8s/
└── charts/
    └── subscription-service/
        ├── Chart.yaml
        ├── values.yaml
        ├── values-dev.yaml
        ├── values-prod.yaml
        └── templates/
            ├── hapi-{deployment,service,configmap}.yaml
            ├── matchbox-{deployment,service,configmap}.yaml
            ├── ipf-{deployment,service}.yaml      ← Service: LoadBalancer for MLLP
            ├── postgres-{statefulset,service,pvc}.yaml
            ├── ingress.yaml                       ← FHIR HTTPS ingress
            └── cloudflared-{deployment,configmap,secret}.yaml
```

Same images as the Docker Compose target. Per-environment overrides via `values-*.yaml`.
