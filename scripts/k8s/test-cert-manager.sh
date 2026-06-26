#!/usr/bin/env bash
# Template-time tests for the cert-manager integration on the
# subscription-service ingress (ticket #415). These tests verify chart
# behavior via `helm template` + grep assertions — no live cluster needed.
#
# Run from the repo root:
#   scripts/k8s/test-cert-manager.sh
set -euo pipefail

CHART=deploy/k8s/charts/subscription-service

# 1. Default: no cert-manager annotations
helm template t $CHART > /tmp/t1.yaml
! grep -q "cert-manager.io" /tmp/t1.yaml || { echo "FAIL: cert-manager annotation leaked into default render"; exit 1; }
echo "PASS 1: default render has no cert-manager annotations"

# 2. Opt-in with clusterIssuer
helm template t $CHART --set ingress.certManager.enabled=true \
  --set ingress.certManager.clusterIssuer=letsencrypt-prod > /tmp/t2.yaml
grep -q 'cert-manager.io/cluster-issuer: letsencrypt-prod' /tmp/t2.yaml || { echo "FAIL: clusterIssuer annotation missing"; exit 1; }
grep -q "secretName:.*-tls" /tmp/t2.yaml || { echo "FAIL: tls block not auto-populated"; exit 1; }
echo "PASS 2: clusterIssuer opt-in adds annotation + auto-populates tls"

# 3. Opt-in with namespaced Issuer
helm template t $CHART --set ingress.certManager.enabled=true \
  --set ingress.certManager.issuer=my-issuer > /tmp/t3.yaml
grep -q 'cert-manager.io/issuer: my-issuer' /tmp/t3.yaml || { echo "FAIL: namespaced issuer annotation missing"; exit 1; }
echo "PASS 3: namespaced Issuer opt-in adds correct annotation"

# 4. Opt-in + operator-supplied tls block is left alone
helm template t $CHART --set ingress.certManager.enabled=true \
  --set ingress.certManager.clusterIssuer=letsencrypt-prod \
  --set 'ingress.tls[0].secretName=my-existing-cert' \
  --set 'ingress.tls[0].hosts[0]=fhir.example.com' > /tmp/t4.yaml
grep -q 'secretName: my-existing-cert' /tmp/t4.yaml || { echo "FAIL: operator-supplied tls block was overridden"; exit 1; }
echo "PASS 4: operator-supplied tls block is preserved"

# 5. Fail-fast when neither clusterIssuer nor issuer is set
if helm template t $CHART --set ingress.certManager.enabled=true 2>/dev/null; then
  echo "FAIL: expected helm template to error when no issuer is set"
  exit 1
fi
echo "PASS 5: fail-fast triggers when enabled with no issuer set"

echo "all cert-manager template tests passed"
