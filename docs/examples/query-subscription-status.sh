#!/usr/bin/env bash
# query-subscription-status.sh
#
# Show a Subscription's current state and its full history. Use this to debug
# "is my subscription active?" or "when did it transition to error?"
#
# Env vars:
#   SUBSCRIPTION_SERVICE_URL  FHIR base URL, no trailing slash.
#                             Default: https://subscription-service.example.com/fhir
#   TOKEN                     OAuth2 bearer.
#                             Required unless the deployment has auth disabled.
#
# Args:
#   $1  Subscription id (required)
#
# Exit codes:
#   0  Subscription found
#   1  Bad args
#   2  Subscription not found / server error

set -euo pipefail

if [[ "${1:-}" == "" || "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  cat <<EOF >&2
usage: $0 <subscription-id>

Reads:
  GET {SUBSCRIPTION_SERVICE_URL}/Subscription/{id}
  GET {SUBSCRIPTION_SERVICE_URL}/Subscription/{id}/_history
EOF
  exit 1
fi

SUB_ID="$1"

: "${SUBSCRIPTION_SERVICE_URL:=https://subscription-service.example.com/fhir}"
: "${TOKEN:=}"

CURRENT=$(mktemp); HISTORY=$(mktemp)
trap 'rm -f "$CURRENT" "$HISTORY"' EXIT

if [[ -n "$TOKEN" ]]; then
  CURRENT_CODE=$(curl -sS -o "$CURRENT" -w '%{http_code}' \
    -H "Authorization: Bearer ${TOKEN}" \
    "${SUBSCRIPTION_SERVICE_URL}/Subscription/${SUB_ID}")
else
  CURRENT_CODE=$(curl -sS -o "$CURRENT" -w '%{http_code}' \
    "${SUBSCRIPTION_SERVICE_URL}/Subscription/${SUB_ID}")
fi

if [[ "$CURRENT_CODE" != "200" ]]; then
  echo "Subscription/${SUB_ID}: HTTP ${CURRENT_CODE}" >&2
  cat "$CURRENT" >&2
  echo >&2
  exit 2
fi

echo "=== Subscription/${SUB_ID} (current) ==="
jq '{
  id,
  status,
  criteria,
  reason,
  endpoint: .channel.endpoint,
  channel_type: .channel.type,
  payload: .channel.payload,
  error,
  lastUpdated: .meta.lastUpdated,
  versionId: .meta.versionId
}' < "$CURRENT"

if [[ -n "$TOKEN" ]]; then
  curl -sS -o "$HISTORY" \
    -H "Authorization: Bearer ${TOKEN}" \
    "${SUBSCRIPTION_SERVICE_URL}/Subscription/${SUB_ID}/_history"
else
  curl -sS -o "$HISTORY" \
    "${SUBSCRIPTION_SERVICE_URL}/Subscription/${SUB_ID}/_history"
fi

echo
echo "=== Subscription/${SUB_ID}/_history ==="
jq '[.entry[]?.resource | {
  versionId: .meta.versionId,
  status,
  lastUpdated: .meta.lastUpdated,
  error
}] | reverse' < "$HISTORY"
