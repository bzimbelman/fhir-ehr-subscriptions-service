#!/usr/bin/env bash
# scripts/k8s/test-psa.sh
#
# Pod Security Standards `restricted` profile compliance test for the
# subscription-service Helm chart. Renders the chart with `helm template`
# and verifies every Pod template and every container satisfies the
# `restricted` profile's hard requirements:
#
#   1. pod.spec.securityContext.runAsNonRoot == true
#   2. pod.spec.securityContext.seccompProfile.type == RuntimeDefault
#   3. container.securityContext.allowPrivilegeEscalation == false
#   4. container.securityContext.capabilities.drop contains "ALL"
#   5. container.securityContext.runAsNonRoot == true (or inherited)
#
# Run from the repo root:
#   scripts/k8s/test-psa.sh
#
# Exits non-zero if any check fails. Uses Python (PyYAML) rather than yq
# because PyYAML is everywhere and yq isn't always installed on dev laptops.
#
# Ticket #420.

set -euo pipefail

CHART="${CHART:-deploy/k8s/charts/subscription-service}"
RENDERED="${TMPDIR:-/tmp}/psa-rendered.yaml"

if [ ! -d "$CHART" ]; then
    echo "FAIL: chart directory not found at $CHART" >&2
    exit 2
fi

helm template t "$CHART" >"$RENDERED"

python3 - "$RENDERED" <<'PY'
import sys
import yaml

path = sys.argv[1]
with open(path) as f:
    docs = list(yaml.safe_load_all(f))

failures = []

POD_OWNERS = {"Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob"}


def check_pod(kind: str, name: str, pod_spec: dict) -> None:
    """Validate a PodSpec against PSA `restricted`."""
    sc = pod_spec.get("securityContext") or {}
    label = f"{kind}/{name}"

    # Pod-level: runAsNonRoot true
    if sc.get("runAsNonRoot") is not True:
        failures.append(f"{label}: pod-level securityContext.runAsNonRoot must be true (got {sc.get('runAsNonRoot')!r})")

    # Pod-level: seccompProfile.type == RuntimeDefault (or set on every container)
    pod_seccomp = (sc.get("seccompProfile") or {}).get("type")
    pod_seccomp_ok = pod_seccomp == "RuntimeDefault"

    init = pod_spec.get("initContainers") or []
    main = pod_spec.get("containers") or []
    for container in init + main:
        cname = container.get("name", "?")
        csc = container.get("securityContext") or {}
        clabel = f"{label}#{cname}"

        # Container-level: allowPrivilegeEscalation false
        if csc.get("allowPrivilegeEscalation") is not False:
            failures.append(f"{clabel}: container securityContext.allowPrivilegeEscalation must be false (got {csc.get('allowPrivilegeEscalation')!r})")

        # Container-level: capabilities.drop contains ALL
        drops = ((csc.get("capabilities") or {}).get("drop")) or []
        if "ALL" not in drops:
            failures.append(f"{clabel}: container capabilities.drop must contain 'ALL' (got {drops!r})")

        # Container-level: runAsNonRoot true (or inherited from pod where pod says true)
        crnr = csc.get("runAsNonRoot")
        if crnr is False:
            failures.append(f"{clabel}: container runAsNonRoot must not be false when pod is restricted")

        # seccompProfile: either pod-level or container-level RuntimeDefault.
        cseccomp = (csc.get("seccompProfile") or {}).get("type")
        if not pod_seccomp_ok and cseccomp != "RuntimeDefault":
            failures.append(f"{clabel}: seccompProfile.type must be RuntimeDefault (pod: {pod_seccomp!r}, container: {cseccomp!r})")


for doc in docs:
    if not isinstance(doc, dict):
        continue
    kind = doc.get("kind")
    if kind not in POD_OWNERS:
        continue
    name = (doc.get("metadata") or {}).get("name", "?")
    spec = doc.get("spec") or {}
    template = spec.get("template") or {}
    pod_spec = template.get("spec") or {}
    if not pod_spec:
        failures.append(f"{kind}/{name}: no pod spec found")
        continue
    check_pod(kind, name, pod_spec)

if failures:
    print("PSA `restricted` compliance: FAILED")
    for f in failures:
        print(f"  - {f}")
    sys.exit(1)
else:
    print("PSA `restricted` compliance: all pod templates and containers OK")
PY
