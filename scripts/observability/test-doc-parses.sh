#!/usr/bin/env bash
#
# Smoke test for the log/metric schema docs (ticket #397).
#
# Asserts:
#   1. The doc parser runs cleanly against docs/observability/log-schema.md.
#   2. The log-schema doc extracts >= 9 fields (one row per the v1.0 matrix).
#   3. Every fenced ```json block in the log-schema doc parses as JSON.
#   4. The doc parser runs cleanly against docs/observability/metric-catalog.md.
#   5. The metric catalog extracts >= 5 metric rows.
#
# This is the lightweight check that's wired into the repo today. The
# heavier CI gate (Testcontainers + JSON log capture + Prometheus scrape)
# is described in docs/observability/schema-stability-contract.md and is
# deferred to a follow-up ticket.

set -euo pipefail

# Resolve the repo root from this script's location so the test runs no
# matter where it's invoked from.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "${REPO_ROOT}"

PARSER="${SCRIPT_DIR}/parse-log-schema.py"
LOG_DOC="docs/observability/log-schema.md"
METRIC_DOC="docs/observability/metric-catalog.md"

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

ok() {
    echo "ok: $*"
}

# ---------------------------------------------------------------------------
# 1. log-schema parses & matrix has rows.
# ---------------------------------------------------------------------------
test -f "${LOG_DOC}" || fail "${LOG_DOC} not found"
LOG_JSON="$(python3 "${PARSER}" "${LOG_DOC}")"
LOG_COUNT="$(echo "${LOG_JSON}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["count"])')"
if [ "${LOG_COUNT}" -lt 9 ]; then
    echo "${LOG_JSON}" >&2
    fail "log-schema: expected >= 9 field rows, got ${LOG_COUNT}"
fi
ok "log-schema.md parsed cleanly (${LOG_COUNT} fields)"

# ---------------------------------------------------------------------------
# 2. Worked-example JSON blocks parse.
# ---------------------------------------------------------------------------
python3 "${PARSER}" "${LOG_DOC}" --validate-examples > /tmp/log-schema-validate.json || {
    cat /tmp/log-schema-validate.json >&2
    fail "log-schema: a worked-example JSON block is invalid"
}
ok "log-schema.md worked-example JSON blocks all parse"

# ---------------------------------------------------------------------------
# 3. metric-catalog parses & has rows.
# ---------------------------------------------------------------------------
test -f "${METRIC_DOC}" || fail "${METRIC_DOC} not found"
METRIC_JSON="$(python3 "${PARSER}" "${METRIC_DOC}")"
METRIC_COUNT="$(echo "${METRIC_JSON}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["count"])')"
if [ "${METRIC_COUNT}" -lt 5 ]; then
    echo "${METRIC_JSON}" >&2
    fail "metric-catalog: expected >= 5 metric rows, got ${METRIC_COUNT}"
fi
ok "metric-catalog.md parsed cleanly (${METRIC_COUNT} metrics)"

echo
echo "All doc-parse smoke tests passed."
