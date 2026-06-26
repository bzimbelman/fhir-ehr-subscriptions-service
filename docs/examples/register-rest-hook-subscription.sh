#!/usr/bin/env bash
# register-rest-hook-subscription.sh
#
# Register a legacy R4 criteria-based Subscription with a rest-hook channel
# against any subscription-service deployment.
#
# Env vars:
#   SUBSCRIPTION_SERVICE_URL  FHIR base URL, no trailing slash.
#                             Default: https://subscription-service.example.com/fhir
#   TOKEN                     OAuth2 bearer for the subscription-service realm.
#                             Required unless the deployment has auth disabled.
#   CALLBACK_URL              HTTPS URL where notifications should be delivered.
#                             Required. Try https://webhook.site/<uuid> to play.
#   CALLBACK_SECRET           Bearer your callback will check on incoming
#                             notifications. Default: a random 32-char hex.
#   CRITERIA                  FHIR search expression describing what to watch.
#                             Default: "Patient?" (any Patient change).
#   PAYLOAD                   Notification payload format. One of:
#                                "application/fhir+json"  (default; PUT with body)
#                                "application/fhir+xml"
#                                ""                       (id-only POST, no body)
#
# Exit codes:
#   0  Subscription created (id printed to stdout)
#   1  Missing required env or non-2xx response from the server

set -euo pipefail

: "${SUBSCRIPTION_SERVICE_URL:=https://subscription-service.example.com/fhir}"
: "${TOKEN:=}"
: "${CALLBACK_URL:?CALLBACK_URL is required (try https://webhook.site/<uuid>)}"
: "${CALLBACK_SECRET:=$(openssl rand -hex 16 2>/dev/null || echo change-me-$(date +%s))}"
: "${CRITERIA:=Patient?}"
: "${PAYLOAD:=application/fhir+json}"

PAYLOAD_FIELD=""
if [[ -n "$PAYLOAD" ]]; then
  PAYLOAD_FIELD=$(printf '"payload": "%s",' "$PAYLOAD")
fi

# Build curl auth arg as a single flag value to avoid bash 3.2 set -u array
# unbound-variable issues. The Authorization header is left off entirely when
# TOKEN is empty (useful when running against a deployment with auth disabled).

BODY=$(cat <<EOF
{
  "resourceType": "Subscription",
  "status": "requested",
  "reason": "registered via register-rest-hook-subscription.sh",
  "criteria": "${CRITERIA}",
  "channel": {
    "type": "rest-hook",
    "endpoint": "${CALLBACK_URL}",
    ${PAYLOAD_FIELD}
    "header": ["Authorization: Bearer ${CALLBACK_SECRET}"]
  }
}
EOF
)

echo "POST ${SUBSCRIPTION_SERVICE_URL}/Subscription" >&2
echo "  criteria=${CRITERIA}" >&2
echo "  endpoint=${CALLBACK_URL}" >&2
echo "  payload=${PAYLOAD:-<id-only>}" >&2

RESP=$(mktemp)
trap 'rm -f "$RESP"' EXIT

if [[ -n "$TOKEN" ]]; then
  HTTP_CODE=$(curl -sS -o "$RESP" -w '%{http_code}' \
    -X POST "${SUBSCRIPTION_SERVICE_URL}/Subscription" \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Content-Type: application/fhir+json" \
    -d "$BODY")
else
  HTTP_CODE=$(curl -sS -o "$RESP" -w '%{http_code}' \
    -X POST "${SUBSCRIPTION_SERVICE_URL}/Subscription" \
    -H "Content-Type: application/fhir+json" \
    -d "$BODY")
fi

if [[ "$HTTP_CODE" != "201" && "$HTTP_CODE" != "200" ]]; then
  echo "ERROR: server returned HTTP ${HTTP_CODE}" >&2
  cat "$RESP" >&2
  echo >&2
  exit 1
fi

SUB_ID=$(jq -r '.id' < "$RESP")
echo "Subscription created: id=${SUB_ID}" >&2
echo "Callback bearer (verify this on your endpoint): ${CALLBACK_SECRET}" >&2
echo "${SUB_ID}"
