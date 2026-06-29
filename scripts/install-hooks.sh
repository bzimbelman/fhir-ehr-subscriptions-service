#!/usr/bin/env sh
# Epic #547 #548 — one-time hook installer
# Points git at the in-repo .githooks/ directory.
set -e

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

if [ ! -d .githooks ]; then
  echo "[install-hooks] .githooks/ not found in repo root. Aborting."
  exit 1
fi

git config core.hooksPath .githooks
chmod +x .githooks/pre-commit .githooks/pre-push 2>/dev/null || true

echo "[install-hooks] core.hooksPath set to .githooks"
echo "[install-hooks] Verify gitleaks is installed:"
if command -v gitleaks >/dev/null 2>&1; then
  echo "[install-hooks]   gitleaks $(gitleaks version 2>/dev/null || echo unknown)"
else
  echo "[install-hooks]   NOT installed. Run: brew install gitleaks"
fi
echo "[install-hooks] Test: gitleaks detect --config .gitleaks.toml --no-banner"
