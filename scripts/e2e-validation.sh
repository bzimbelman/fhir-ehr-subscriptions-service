#!/usr/bin/env bash
# e2e-validation.sh — end-to-end exercise of SUBSCRIPTION_SERVICE_VALIDATION_MODE.
#
# Ticket: #367. Brings up an isolated HAPI + Postgres stack (compose project
# `subsvc-validation-test`, host ports 48080/48081/48090/42575) and walks
# through the three modes by restarting HAPI between them:
#
#   1. mode=off     — POST a non-conforming Patient. Expect 201, no validation issues.
#   2. mode=warn    — POST the same Patient. Expect 201, response OperationOutcome
#                      surfaces warning/error severities.
#   3. mode=enforce — POST the same Patient. Expect 422, OperationOutcome
#                      lists the issues.
#
# Auth is disabled for this e2e (SUBSCRIPTION_SERVICE_AUTH_ENABLED=false) so
# the script doesn't need a live Keycloak — it's exclusively exercising the
# validation interceptor.
#
# Tear-down happens at the end (and on Ctrl-C via a trap). The stack lives
# in deploy/docker; we override the compose project name and port mappings
# so a parallel production-shaped stack on the same host doesn't collide.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
COMPOSE_DIR="${REPO_ROOT}/deploy/docker"

# Isolated compose project — does NOT touch the default `subscription-service`
# stack if one happens to be running.
export COMPOSE_PROJECT_NAME="subsvc-validation-test"

# Ports chosen so they don't collide with the default 18080/18081/18090/2575
# stack, the cdstools-deployment stacks (3xxxx), or any reference
# subscription-service stack that might be running on the same host.
export HAPI_HOST_PORT=48080
export MATCHBOX_HOST_PORT=48081
export INTERFACE_ENGINE_HTTP_HOST_PORT=48090
export INTERFACE_ENGINE_MLLP_HOST_PORT=42575

# Postgres data lives in a project-scoped subdir so tearing down `subsvc-
# validation-test` doesn't trample the dev stack's data.
export POSTGRES_DATA_DIR="${COMPOSE_DIR}/postgres-data-validation-test"

# Auth off — we're only exercising validation.
export SUBSCRIPTION_SERVICE_AUTH_ENABLED=false
# Issuer/JWKS still need *something* to satisfy property binding; bogus
# values are fine because the interceptor never runs.
export SUBSCRIPTION_SERVICE_AUTH_ISSUER="https://keycloak.example.invalid/realms/none"

HAPI_BASE="http://localhost:${HAPI_HOST_PORT}/fhir"

# Canonical "fails US Core validation" Patient — claims the us-core-patient
# profile but is missing the required identifier slice (US Core requires 1..*
# identifier on Patient).
NON_CONFORMING_PATIENT='{
  "resourceType": "Patient",
  "meta": {
    "profile": ["http://hl7.org/fhir/us/core/StructureDefinition/us-core-patient"]
  },
  "name": [{"given": ["NoFamily"]}]
}'

log() {
  printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*" >&2
}

# shellcheck disable=SC2329  # invoked via `trap teardown EXIT` below
teardown() {
  log "tearing down compose project ${COMPOSE_PROJECT_NAME}"
  (cd "${COMPOSE_DIR}" && docker compose down -v --remove-orphans >/dev/null 2>&1 || true)
  rm -rf "${POSTGRES_DATA_DIR}" 2>/dev/null || true
}
trap teardown EXIT

ensure_igs() {
  if [[ ! -f "${REPO_ROOT}/hapi/igs/hl7.fhir.us.core-7.0.0.tgz" ]]; then
    log "fetching IG packages"
    "${REPO_ROOT}/scripts/fetch-igs.sh" >&2
  fi
}

wait_for_hapi() {
  local attempts=60
  local i=0
  while (( i < attempts )); do
    if curl -sf -o /dev/null "${HAPI_BASE}/metadata"; then
      log "HAPI is up at ${HAPI_BASE}"
      return 0
    fi
    sleep 2
    i=$(( i + 1 ))
  done
  log "HAPI did not become healthy after $(( attempts * 2 ))s"
  log "--- HAPI logs (tail) ---"
  (cd "${COMPOSE_DIR}" && docker compose logs --tail=80 hapi >&2 || true)
  return 1
}

# Boot the stack with the given validation mode. We use --no-deps + a fresh
# `up -d hapi hapi-db` so we don't drag the interface-engine / Matchbox
# services in (they aren't relevant to validation and slow the test down).
bring_up_with_mode() {
  local mode="$1"
  log "==> starting stack with SUBSCRIPTION_SERVICE_VALIDATION_MODE=${mode}"
  export SUBSCRIPTION_SERVICE_VALIDATION_MODE="${mode}"
  (cd "${COMPOSE_DIR}" \
    && docker compose up -d --build --force-recreate hapi-db hapi >/dev/null 2>&1)
  wait_for_hapi
}

# POSTs the non-conforming Patient and prints the HTTP status code + the
# trimmed OperationOutcome severities found in the response body.
post_non_conforming_patient() {
  local label="$1"
  log "POST /Patient (${label})"
  local body_file status_file
  body_file="$(mktemp)"
  status_file="$(mktemp)"
  set +e
  # Prefer: return=OperationOutcome tells HAPI to return the validation
  # OperationOutcome instead of the persisted resource on a successful CREATE.
  # Without it, the response body in mode=off / mode=warn is just the created
  # Patient — the validation findings are computed and (per
  # addValidationResultsToResponseOperationOutcome=true) attached, but they
  # only become visible when the response shape is the OperationOutcome.
  # In mode=enforce the response is always an OperationOutcome regardless,
  # because the request never succeeds.
  curl -s -o "${body_file}" -w '%{http_code}\n' \
    -H 'Content-Type: application/fhir+json' \
    -H 'Prefer: return=OperationOutcome' \
    -X POST \
    -d "${NON_CONFORMING_PATIENT}" \
    "${HAPI_BASE}/Patient" >"${status_file}"
  set -e

  local status
  status="$(cat "${status_file}")"
  # All human-readable transcript output goes to stderr so the caller can
  # capture the raw HTTP code from stdout via $(...). Otherwise the multi-line
  # decorations get conflated with the status code and the script silently
  # uses "    HTTP 201" as if it were "201".
  printf '    HTTP %s\n' "${status}" >&2

  # Extract OperationOutcome severities, if any, without depending on jq.
  # `python3 -m json.tool` would do it, but a simple grep is enough for the
  # transcript output we want here. The `|| true` is load-bearing: with
  # `set -euo pipefail`, the pipeline's exit status comes from the LEFTMOST
  # failing command — and grep returns 1 when no matches are found (the
  # mode=off case, where the response body has no OperationOutcome). Without
  # the override, the surrounding `$(...)` command substitution propagates
  # that 1 and the script silently aborts.
  local severities
  severities="$( (grep -oE '"severity":[[:space:]]*"[^"]+"' "${body_file}" || true) | sort -u | tr '\n' ',' | sed 's/,$//')"
  if [[ -n "${severities}" ]]; then
    printf '    OperationOutcome severities present: %s\n' "${severities}" >&2
  else
    printf '    OperationOutcome: no validation issues\n' >&2
  fi

  rm -f "${body_file}" "${status_file}"
  printf '%s\n' "${status}"
}

# -------------- main flow --------------

ensure_igs

log "tearing down any prior ${COMPOSE_PROJECT_NAME} stack"
(cd "${COMPOSE_DIR}" && docker compose down -v --remove-orphans >/dev/null 2>&1 || true)
rm -rf "${POSTGRES_DATA_DIR}"

results_ok=true

# ---- 1. mode=off ----
bring_up_with_mode "off"
status_off="$(post_non_conforming_patient "mode=off")"
if [[ "${status_off}" != "201" ]]; then
  log "FAIL: mode=off expected 201, got ${status_off}"
  results_ok=false
fi

# ---- 2. mode=warn ----
log "restarting HAPI with mode=warn"
export SUBSCRIPTION_SERVICE_VALIDATION_MODE="warn"
(cd "${COMPOSE_DIR}" && docker compose up -d --force-recreate hapi >/dev/null 2>&1)
wait_for_hapi
status_warn="$(post_non_conforming_patient "mode=warn")"
if [[ "${status_warn}" != "201" ]]; then
  log "FAIL: mode=warn expected 201, got ${status_warn}"
  results_ok=false
fi

# ---- 3. mode=enforce ----
log "restarting HAPI with mode=enforce"
export SUBSCRIPTION_SERVICE_VALIDATION_MODE="enforce"
(cd "${COMPOSE_DIR}" && docker compose up -d --force-recreate hapi >/dev/null 2>&1)
wait_for_hapi
status_enforce="$(post_non_conforming_patient "mode=enforce")"
if [[ "${status_enforce}" != "422" ]]; then
  log "FAIL: mode=enforce expected 422, got ${status_enforce}"
  results_ok=false
fi

if $results_ok; then
  log "PASS: all three validation modes behaved as expected"
  exit 0
else
  log "FAIL: see transcript above"
  exit 1
fi
