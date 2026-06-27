#!/usr/bin/env bash
#
# Placeholder for the future CI gate that enforces the log + metric
# stability contract (ticket #397, follow-up implementation).
#
# What this WILL do once implemented (see
# docs/observability/schema-stability-contract.md for the full design):
#
#   1. Parse docs/observability/log-schema.md and metric-catalog.md
#      into a structured representation.
#   2. Run the test suite with logs captured to a buffer.
#   3. Scrape /actuator/prometheus from a Testcontainers harness.
#   4. Assert every REQUIRED field is present on every captured log record.
#   5. Assert every REQUIRED metric appears in the scrape.
#   6. Fail the build if either check fails.
#
# Today: this is a stub. It prints a message and exits 0 so it can be
# safely wired into CI as a non-blocking check now, then becomes blocking
# when the real implementation lands.
#
# The doc-parse smoke test (the lightweight piece of this work that
# actually does run) is scripts/observability/test-doc-parses.sh.

set -euo pipefail

cat <<'EOF'
check-log-schema.sh: not yet implemented.

This script is the placeholder for the future schema-stability CI gate
described in docs/observability/schema-stability-contract.md.

Run scripts/observability/test-doc-parses.sh for the doc-parse smoke
test that's wired in today.

Exiting 0 (non-blocking placeholder).
EOF

exit 0
