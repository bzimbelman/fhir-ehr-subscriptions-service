# scripts

Operational helpers for subscription-service. Lifecycle, smoke tests, IG fetching, etc.

Scripts here should:

- Be shell-only (bash) where possible, so they run on any developer machine.
- Use `set -euo pipefail` and exit non-zero on failure.
- Document expected env vars at the top.
- Live alongside, not inside, the application code in `interface-engine/`.
