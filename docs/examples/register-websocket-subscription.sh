#!/usr/bin/env bash
# register-websocket-subscription.sh
#
# Register a Subscription with channel.type=websocket. Unlike rest-hook, no
# endpoint is configured on the Subscription itself; the subscriber opens a
# WebSocket connection to the server's websocket endpoint and binds it to the
# Subscription ID after the resource is created.
#
# Env vars:
#   SUBSCRIPTION_SERVICE_URL  FHIR base URL, no trailing slash.
#                             Default: https://subscription-service.example.com/fhir
#   TOKEN                     OAuth2 bearer for the subscription-service realm.
#                             Required unless the deployment has auth disabled.
#   CRITERIA                  FHIR search expression. Default: "Patient?".
#   PAYLOAD                   Payload format the WS messages will carry. Default
#                             is "application/fhir+json"; set "" for id-only
#                             ping messages.
#
# Exit codes:
#   0  Subscription created (id printed to stdout)
#   1  Non-2xx response from the server

set -euo pipefail

: "${SUBSCRIPTION_SERVICE_URL:=https://subscription-service.example.com/fhir}"
: "${TOKEN:=}"
: "${CRITERIA:=Patient?}"
: "${PAYLOAD:=application/fhir+json}"

PAYLOAD_FIELD=""
if [[ -n "$PAYLOAD" ]]; then
  PAYLOAD_FIELD=$(printf ', "payload": "%s"' "$PAYLOAD")
fi

BODY=$(cat <<EOF
{
  "resourceType": "Subscription",
  "status": "requested",
  "reason": "registered via register-websocket-subscription.sh",
  "criteria": "${CRITERIA}",
  "channel": {
    "type": "websocket"${PAYLOAD_FIELD}
  }
}
EOF
)

echo "POST ${SUBSCRIPTION_SERVICE_URL}/Subscription (websocket channel)" >&2
echo "  criteria=${CRITERIA}" >&2
echo "  payload=${PAYLOAD:-<id-only ping>}" >&2

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

# Derive ws(s):// host from the FHIR base URL.
WS_BASE=$(echo "$SUBSCRIPTION_SERVICE_URL" | sed -E 's|^https://|wss://|; s|^http://|ws://|; s|/fhir/?$||')

cat >&2 <<NOTICE

Subscription created: id=${SUB_ID}

To receive notifications, open a WebSocket connection:
  ${WS_BASE}/websocket

Then send the bind frame (text, no quoting):
  bind ${SUB_ID}

The server replies "bound ${SUB_ID}" and from that point will push
"ping ${SUB_ID}" each time the Subscription fires.${PAYLOAD:+
With payload=${PAYLOAD}, each ping is followed by a message carrying
the resource body in that MIME type.}

Quick test with wscat (npm i -g wscat):
  TOKEN='${TOKEN:-<your-token>}'
  wscat -c ${WS_BASE}/websocket \\
    -H "Authorization: Bearer \$TOKEN" \\
    --execute "bind ${SUB_ID}"

NOTICE

echo "${SUB_ID}"
