#!/usr/bin/env bash
# OP #155: generate the demo's at-rest AES-GCM key on first-time setup.
#
# The bridge reads the key bytes via ${file:/etc/fhir-subs/secrets/at_rest_key}
# (cmd/fhir-subs/config.go ${file:/path} interpolation). docker-compose
# mounts demo/secrets/ into /etc/fhir-subs/secrets/ inside the bridge
# container, so writing the file here is enough.
#
# This script is idempotent: re-running it does NOT overwrite an
# existing key. To rotate, delete the file first.
#
# Usage:
#   demo/scripts/generate-keys.sh
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
secrets_dir="$(cd -- "${script_dir}/../secrets" && pwd)"
key_path="${secrets_dir}/at_rest_key"

if [[ -s "${key_path}" ]]; then
    echo "demo: at_rest_key already present at ${key_path}" >&2
    exit 0
fi

# 32 random bytes -> base64 (44-char), trailing newline stripped so the
# file contents are exactly the codec material the bridge expects.
if command -v openssl >/dev/null 2>&1; then
    openssl rand -base64 32 | tr -d '\n' > "${key_path}"
else
    # Fallback: /dev/urandom + base64 (mac/linux both ship base64).
    head -c 32 /dev/urandom | base64 | tr -d '\n' > "${key_path}"
fi
# Mode 0644 (NOT 0600) so the bridge container's nonroot user can read
# the bind-mounted file. docker-compose mounts the host file in
# read-only mode (`./secrets:...:ro`), and the demo key is not a real
# secret — `demo/secrets/.gitignore` keeps the bytes out of VCS, but
# anyone with shell access to the host can already read demo/config.yaml
# itself. Production deployments should use a proper secret manager.
chmod 644 "${key_path}"

echo "demo: generated at_rest_key at ${key_path}" >&2
