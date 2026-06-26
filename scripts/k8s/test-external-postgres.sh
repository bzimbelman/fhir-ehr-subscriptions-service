#!/usr/bin/env bash
# Template-time tests for ticket #416 (external Postgres).
#
# Validates four scenarios with `helm template`:
#   1. Default (in-cluster Postgres) — StatefulSet + Secret render.
#   2. externalPostgres.enabled with everything set — in-cluster resources
#      are skipped and HAPI's env references the external host + Secret.
#   3. externalPostgres.enabled with empty host — template fails with a
#      clear error.
#   4. externalPostgres.enabled with empty passwordSecret — template fails
#      with a clear error.
#
# Run from the repo root: bash scripts/k8s/test-external-postgres.sh
set -euo pipefail
CHART=deploy/k8s/charts/subscription-service

# 1. Default: in-cluster Postgres renders
helm template t $CHART > /tmp/t1.yaml
grep -q "kind: StatefulSet" /tmp/t1.yaml || { echo "FAIL: in-cluster Postgres StatefulSet missing"; exit 1; }
grep -q "name: t-postgres" /tmp/t1.yaml || { echo "FAIL: postgres secret missing"; exit 1; }

# 2. externalPostgres: enabled with everything set — in-cluster resources skipped
helm template t $CHART --set externalPostgres.enabled=true \
  --set externalPostgres.host=ext.example.com \
  --set externalPostgres.passwordSecret=my-pw > /tmp/t2.yaml
! grep -q "kind: StatefulSet" /tmp/t2.yaml || { echo "FAIL: StatefulSet rendered when external enabled"; exit 1; }
! grep -q '^  name: t-postgres$' /tmp/t2.yaml || { echo "FAIL: in-cluster postgres Secret rendered when external enabled"; exit 1; }
grep -q 'jdbc:postgresql://ext.example.com:5432' /tmp/t2.yaml || { echo "FAIL: external JDBC URL not in HAPI env"; exit 1; }
grep -q 'name: my-pw' /tmp/t2.yaml || { echo "FAIL: external passwordSecret not referenced"; exit 1; }

# 3. externalPostgres.enabled with empty host — template fails
if helm template t $CHART --set externalPostgres.enabled=true --set externalPostgres.passwordSecret=my-pw 2>/dev/null; then
  echo "FAIL: expected template error when host is empty"
  exit 1
fi

# 4. externalPostgres.enabled with empty passwordSecret — template fails
if helm template t $CHART --set externalPostgres.enabled=true --set externalPostgres.host=ext.example.com 2>/dev/null; then
  echo "FAIL: expected template error when passwordSecret is empty"
  exit 1
fi

echo "all external-postgres template tests passed"
