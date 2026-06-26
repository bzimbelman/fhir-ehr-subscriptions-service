#!/usr/bin/env bash
# provision-realm.sh
#
# Idempotently provisions the `subscription-service` Keycloak realm from
# `keycloak/realms/subscription-service.json`.
#
# Environment variables (required when actually running against a server):
#   KEYCLOAK_URL              Base URL, e.g. https://your-keycloak.example.com
#   KEYCLOAK_ADMIN_USER       Master-realm admin username (e.g. admin)
#   KEYCLOAK_ADMIN_PASSWORD   Master-realm admin password
#
# Optional:
#   KEYCLOAK_REALM_FILE       Path to the realm export JSON.
#                             Default: keycloak/realms/subscription-service.json
#                             (resolved relative to the repo root)
#   KEYCLOAK_PATH_PREFIX      Path prefix for the Keycloak server.
#                             Default: "" (Keycloak >= 17 / Quarkus default).
#                             Set to "/auth" for legacy WildFly-based Keycloak
#                             (Keycloak < 17, e.g. the historical the-deploy-host instance).
#   KEYCLOAK_CLIENT_SECRET    When set, the placeholder
#                             "CHANGE-ME-IN-DEPLOYMENT" inside the realm JSON
#                             is rewritten to this value on a temp copy of
#                             the file before import. The committed JSON is
#                             never modified. When unset, the placeholder is
#                             imported as-is and a warning is logged.
#
# Flags:
#   --dry-run                 Authenticate against Keycloak (so the script
#                             still requires valid admin credentials), print
#                             the would-be requests, but DO NOT POST the
#                             realm import. Useful for verifying connectivity
#                             and path-prefix shape (e.g. legacy /auth/).
#   -h, --help                Print usage and exit.
#
# Behavior:
#   1. Validates env + the realm JSON parses.
#   2. Optionally substitutes the client secret on a temp file copy.
#   3. Logs into the master realm using password grant on admin-cli.
#   4. GETs /admin/realms/<realm>. If 200 -> realm exists, exit 0 (no-op).
#                                  If 404 -> POST the export to /admin/realms.
#                                  Any other -> error.
#
# Exits non-zero on any failure. Designed to be safe to re-run.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
REALM_FILE_DEFAULT="${REPO_ROOT}/keycloak/realms/subscription-service.json"

REALM_FILE="${KEYCLOAK_REALM_FILE:-${REALM_FILE_DEFAULT}}"
PATH_PREFIX="${KEYCLOAK_PATH_PREFIX-}"
CLIENT_SECRET="${KEYCLOAK_CLIENT_SECRET-}"
DRY_RUN=0

# Placeholder shipped in the realm export. Substituted at import time when
# KEYCLOAK_CLIENT_SECRET is provided; otherwise imported verbatim with a
# loud warning.
CLIENT_SECRET_PLACEHOLDER="CHANGE-ME-IN-DEPLOYMENT"

usage() {
  cat <<EOF
Usage: KEYCLOAK_URL=<url> KEYCLOAK_ADMIN_USER=<user> KEYCLOAK_ADMIN_PASSWORD=<pw> \\
       [KEYCLOAK_REALM_FILE=<path>] [KEYCLOAK_PATH_PREFIX=/auth] \\
       [KEYCLOAK_CLIENT_SECRET=<secret>] \\
       $(basename "$0") [--dry-run]

Idempotently imports the subscription-service realm into a Keycloak server.

Required env:
  KEYCLOAK_URL              Base URL of the Keycloak server (no trailing slash).
                            Example: https://your-keycloak.example.com
  KEYCLOAK_ADMIN_USER       Username with master-realm admin privileges.
  KEYCLOAK_ADMIN_PASSWORD   Password for that user.

Optional env:
  KEYCLOAK_REALM_FILE       Realm export JSON path.
                            Default: ${REALM_FILE_DEFAULT}
  KEYCLOAK_PATH_PREFIX      Server path prefix. Default empty (modern Keycloak,
                            Quarkus, >= 17). Use /auth for legacy WildFly-based
                            Keycloak installations (Keycloak < 17).
  KEYCLOAK_CLIENT_SECRET    Client secret to substitute for the
                            ${CLIENT_SECRET_PLACEHOLDER} placeholder on a temp
                            copy of the realm JSON. Strongly recommended for
                            any non-dev import.

Flags:
  --dry-run                 Authenticate, print would-be requests, skip POST.
  -h, --help                Print this help and exit.

Examples:
  # Modern (Quarkus) Keycloak at https://kc.example.com — no /auth/ prefix
  KEYCLOAK_URL=https://kc.example.com \\
  KEYCLOAK_ADMIN_USER=admin KEYCLOAK_ADMIN_PASSWORD=secret \\
  KEYCLOAK_CLIENT_SECRET=\$(openssl rand -hex 32) \\
    $(basename "$0")

  # Legacy WildFly Keycloak mounted at /auth (Keycloak < 17)
  KEYCLOAK_URL=https://kc.example.com KEYCLOAK_PATH_PREFIX=/auth \\
  KEYCLOAK_ADMIN_USER=admin KEYCLOAK_ADMIN_PASSWORD=secret \\
    $(basename "$0")

  # Dry-run against legacy the-deploy-host Keycloak to verify path-prefix handling
  KEYCLOAK_URL=https://keycloak.bzonfhir.com KEYCLOAK_PATH_PREFIX=/auth \\
  KEYCLOAK_ADMIN_USER=admin KEYCLOAK_ADMIN_PASSWORD=secret \\
    $(basename "$0") --dry-run

Notes:
  - Existing realms are NOT modified. Re-running is a no-op when the realm
    already exists. To re-import, delete the realm in the admin UI first.
  - When KEYCLOAK_CLIENT_SECRET is NOT set, the placeholder
    "${CLIENT_SECRET_PLACEHOLDER}" is imported verbatim and a warning is
    logged. Rotate it (Admin UI -> Clients -> Credentials or via API)
    BEFORE giving the realm to any integrator.
EOF
}

die() {
  echo "ERROR: $*" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

# Parse flags. Keeping it minimal — no getopts because we only have two.
for arg in "$@"; do
  case "${arg}" in
    --dry-run)
      DRY_RUN=1
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "ERROR: unknown argument: ${arg}" >&2
      usage
      exit 2
      ;;
  esac
done

# Pre-flight checks. If required env is missing, print usage and exit 0
# (acceptance criterion: "helpful usage when run without env vars").
if [[ -z "${KEYCLOAK_URL:-}" || -z "${KEYCLOAK_ADMIN_USER:-}" || -z "${KEYCLOAK_ADMIN_PASSWORD:-}" ]]; then
  usage
  exit 0
fi

need_cmd curl
need_cmd jq

[[ -f "${REALM_FILE}" ]] || die "realm file not found: ${REALM_FILE}"
jq -e . "${REALM_FILE}" >/dev/null || die "realm file is not valid JSON: ${REALM_FILE}"

REALM_NAME="$(jq -r '.realm' "${REALM_FILE}")"
[[ -n "${REALM_NAME}" && "${REALM_NAME}" != "null" ]] \
  || die "realm name not found in ${REALM_FILE} (.realm)"

# Substitute the client secret on a temp copy of the realm JSON if requested.
# The committed file is NEVER mutated — that file lives in git with the
# placeholder so the example is reviewable; the import-time substitution is
# automation glue.
IMPORT_FILE="${REALM_FILE}"
TEMP_FILE=""
cleanup() {
  if [[ -n "${TEMP_FILE}" && -f "${TEMP_FILE}" ]]; then
    rm -f "${TEMP_FILE}"
  fi
}
trap cleanup EXIT

if [[ -n "${CLIENT_SECRET}" ]]; then
  TEMP_FILE="$(mktemp -t provision-realm.XXXXXX.json)"
  # Use jq's walk-the-tree (string replace via gsub) so we don't depend on the
  # placeholder appearing in a specific JSON path. Anchored to the literal
  # placeholder string to avoid clobbering anything else.
  jq --arg placeholder "${CLIENT_SECRET_PLACEHOLDER}" \
     --arg secret "${CLIENT_SECRET}" \
     'walk(if type == "string" and . == $placeholder then $secret else . end)' \
     "${REALM_FILE}" > "${TEMP_FILE}" \
     || die "failed to substitute client secret on temp realm file"
  jq -e . "${TEMP_FILE}" >/dev/null \
     || die "client-secret substitution produced invalid JSON"
  IMPORT_FILE="${TEMP_FILE}"
  echo "==> Substituted client secret on temp file (${TEMP_FILE})."
else
  echo "==> WARNING: KEYCLOAK_CLIENT_SECRET not set. The placeholder"
  echo "    '${CLIENT_SECRET_PLACEHOLDER}' will be imported verbatim. Rotate"
  echo "    via the Keycloak admin UI BEFORE giving the realm to anyone."
fi

# Normalize URL pieces.
BASE_URL="${KEYCLOAK_URL%/}${PATH_PREFIX}"
TOKEN_URL="${BASE_URL}/realms/master/protocol/openid-connect/token"
ADMIN_URL="${BASE_URL}/admin/realms"

echo "==> Keycloak base URL:  ${BASE_URL}"
echo "==> Realm to provision: ${REALM_NAME}"
echo "==> Realm file:         ${REALM_FILE}"
if [[ "${DRY_RUN}" -eq 1 ]]; then
  echo "==> DRY RUN: will authenticate and probe, but will NOT POST the import."
fi

echo "==> Authenticating to master realm as ${KEYCLOAK_ADMIN_USER}..."
TOKEN_RESPONSE="$(
  curl -sS -f -X POST "${TOKEN_URL}" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    --data-urlencode "client_id=admin-cli" \
    --data-urlencode "grant_type=password" \
    --data-urlencode "username=${KEYCLOAK_ADMIN_USER}" \
    --data-urlencode "password=${KEYCLOAK_ADMIN_PASSWORD}"
)" || die "admin token request failed against ${TOKEN_URL}"

ACCESS_TOKEN="$(jq -r '.access_token // empty' <<<"${TOKEN_RESPONSE}")"
[[ -n "${ACCESS_TOKEN}" ]] || die "no access_token in token response"

echo "==> Checking whether realm '${REALM_NAME}' already exists..."
CHECK_STATUS="$(
  curl -sS -o /dev/null -w "%{http_code}" \
    -H "Authorization: Bearer ${ACCESS_TOKEN}" \
    "${ADMIN_URL}/${REALM_NAME}"
)"

case "${CHECK_STATUS}" in
  200)
    echo "==> Realm '${REALM_NAME}' already exists. No changes made."
    echo "    To re-import, delete the realm in the Keycloak admin UI and re-run."
    exit 0
    ;;
  404)
    echo "==> Realm '${REALM_NAME}' does not exist. Importing..."
    ;;
  401|403)
    die "auth failed querying realm (HTTP ${CHECK_STATUS}). Check admin user/role."
    ;;
  *)
    die "unexpected status ${CHECK_STATUS} from ${ADMIN_URL}/${REALM_NAME}"
    ;;
esac

if [[ "${DRY_RUN}" -eq 1 ]]; then
  echo "==> DRY RUN: would POST ${IMPORT_FILE} ($(wc -c <"${IMPORT_FILE}") bytes) to:"
  echo "    ${ADMIN_URL}"
  echo "==> DRY RUN: skipping the import. Auth + path-prefix verified."
  exit 0
fi

IMPORT_STATUS="$(
  curl -sS -o /tmp/provision-realm.out -w "%{http_code}" \
    -X POST "${ADMIN_URL}" \
    -H "Authorization: Bearer ${ACCESS_TOKEN}" \
    -H "Content-Type: application/json" \
    --data-binary "@${IMPORT_FILE}"
)"

case "${IMPORT_STATUS}" in
  201|204)
    echo "==> Realm '${REALM_NAME}' imported successfully (HTTP ${IMPORT_STATUS})."
    echo "    Issuer URL: ${BASE_URL}/realms/${REALM_NAME}"
    echo "    JWKS:       ${BASE_URL}/realms/${REALM_NAME}/protocol/openid-connect/certs"
    echo "    Token URL:  ${BASE_URL}/realms/${REALM_NAME}/protocol/openid-connect/token"
    if [[ -z "${CLIENT_SECRET}" ]]; then
      echo
      echo "==> REMINDER: rotate the example client secret. The exported value"
      echo "    '${CLIENT_SECRET_PLACEHOLDER}' is a placeholder, not a credential."
    fi
    ;;
  409)
    echo "==> Realm already exists (race with another import). No changes made."
    ;;
  *)
    echo "Import response body:" >&2
    cat /tmp/provision-realm.out >&2 || true
    die "realm import failed (HTTP ${IMPORT_STATUS})"
    ;;
esac
