#!/usr/bin/env bash
# Validate the bundled Grafana dashboards (Epic #387, ticket #395).
#
# Each *.json under deploy/docker/grafana/dashboards/ is asserted to:
#   1. Parse as valid JSON.
#   2. Carry a non-empty .title and .uid.
#   3. Declare .schemaVersion (Grafana 11 understands 30+; we use 39).
#   4. Declare a non-empty .panels array.
#   5. Have each panel carry .id, .type, .gridPos.
#   6. Have each .targets[].expr present on panels that have targets — this
#      catches accidentally-empty PromQL strings that would silently render
#      as blank panels in Grafana.
#
# Also validates the YAML provisioning files under
# deploy/docker/grafana/provisioning/ and deploy/docker/prometheus/.
#
# Exit code: 0 if all files pass, 1 if any check fails. Designed to be
# called from CI without arguments.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
dashboards_dir="${repo_root}/deploy/docker/grafana/dashboards"
provisioning_dir="${repo_root}/deploy/docker/grafana/provisioning"
prometheus_dir="${repo_root}/deploy/docker/prometheus"

errors=0

if [[ ! -d "${dashboards_dir}" ]]; then
  echo "FAIL: dashboards dir not found: ${dashboards_dir}" >&2
  exit 1
fi

dashboard_count=0
for f in "${dashboards_dir}"/*.json; do
  [[ -e "${f}" ]] || { echo "FAIL: no *.json under ${dashboards_dir}" >&2; exit 1; }
  dashboard_count=$((dashboard_count + 1))

  rel="${f#"${repo_root}/"}"

  if ! jq -e . "${f}" >/dev/null 2>&1; then
    echo "FAIL: ${rel}: invalid JSON" >&2
    errors=$((errors + 1))
    continue
  fi

  # Required top-level fields
  for path in '.title' '.uid' '.schemaVersion' '.panels'; do
    if [[ "$(jq -r "${path} // \"\"" "${f}")" == "" ]]; then
      echo "FAIL: ${rel}: missing ${path}" >&2
      errors=$((errors + 1))
    fi
  done

  # schemaVersion must be a number (Grafana 11 accepts >= 30)
  schema_ok=$(jq -r '(.schemaVersion // 0) | if type == "number" and . >= 30 then "ok" else "bad" end' "${f}")
  if [[ "${schema_ok}" != "ok" ]]; then
    echo "FAIL: ${rel}: schemaVersion must be a number >= 30" >&2
    errors=$((errors + 1))
  fi

  # panels must be a non-empty array
  panel_count=$(jq -r '.panels | if type == "array" then length else -1 end' "${f}")
  if [[ "${panel_count}" -lt 1 ]]; then
    echo "FAIL: ${rel}: .panels is empty or not an array" >&2
    errors=$((errors + 1))
    continue
  fi

  # Each panel must declare id, type, gridPos; any .targets[] must have non-empty .expr
  panel_problems=$(jq -r '
    [.panels[]
      | select(
          (.id == null) or (.type == null) or (.gridPos == null)
          or ((.targets // []) | map(.expr // "" | select(. == "")) | length > 0)
        )
      | (.id // "<no-id>")
    ] | join(",")
  ' "${f}")
  if [[ -n "${panel_problems}" ]]; then
    echo "FAIL: ${rel}: panels with missing id/type/gridPos or empty expr: ${panel_problems}" >&2
    errors=$((errors + 1))
  fi

  title=$(jq -r '.title' "${f}")
  uid=$(jq -r '.uid' "${f}")
  echo "ok: ${rel}: '${title}' (uid=${uid}, ${panel_count} panels)"
done

if [[ "${dashboard_count}" -lt 1 ]]; then
  echo "FAIL: zero dashboards found under ${dashboards_dir}" >&2
  errors=$((errors + 1))
fi

# YAML provisioning files — parse with python3 + PyYAML.
check_yaml() {
  local f="$1"
  local rel="${f#"${repo_root}/"}"
  if ! python3 -c "import sys, yaml; yaml.safe_load(open(sys.argv[1]))" "${f}" 2>/dev/null; then
    echo "FAIL: ${rel}: invalid YAML" >&2
    errors=$((errors + 1))
    return
  fi
  echo "ok: ${rel}: yaml parses"
}

for f in \
  "${provisioning_dir}/datasources/prometheus.yaml" \
  "${provisioning_dir}/dashboards/default.yaml" \
  "${prometheus_dir}/prometheus.yml" \
; do
  if [[ ! -f "${f}" ]]; then
    echo "FAIL: missing: ${f#"${repo_root}/"}" >&2
    errors=$((errors + 1))
    continue
  fi
  check_yaml "${f}"
done

if [[ "${errors}" -ne 0 ]]; then
  echo "FAIL: ${errors} errors" >&2
  exit 1
fi

echo "ok: ${dashboard_count} dashboards, 3 provisioning files validated"
