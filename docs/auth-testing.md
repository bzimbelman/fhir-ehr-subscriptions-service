# Auth testing — operator UI

Quick reference for the test-user accounts used by the multi-user
isolation tests on the maintainer's reference deployment.

## Where the credentials live

The test-user passwords are **NOT** stored in this repository. They live
in the deployment's `.env` file:

- Reference deployment (zdock): `/home/zman/cz/subscription-service/deploy/docker/.env`
- Your deployment: wherever your `deploy/docker/.env` (or k8s `Secret`)
  lives.

The relevant keys:

```
TEST_USER_A_USERNAME=opsa
TEST_USER_A_PASSWORD=opsa-test-pw-2026
TEST_USER_B_USERNAME=opsb
TEST_USER_B_PASSWORD=opsb-test-pw-2026
```

These get loaded by e2e harnesses (Playwright / future ticket #424) so
the test cases don't have to hard-code credentials.

## Test users on the reference deployment

Keycloak realm `Development` on `keycloak.bzonfhir.com` has two
purpose-made test users for the operator UI:

| Username | Email              | Password               | Used by                                    |
| -------- | ------------------ | ---------------------- | ------------------------------------------ |
| `opsa`   | opsa@example.com   | (see `.env`)           | Ticket #423 / #424 multi-user isolation A  |
| `opsb`   | opsb@example.com   | (see `.env`)           | Ticket #423 / #424 multi-user isolation B  |

There is also a `test` / `test` smoke-test credential on the realm.
Prefer `opsa` and `opsb` for any test that exercises the multi-user
flow (sign-in, session isolation, sign-out independence) — the `test`
user is a single account and can't prove isolation.

## Adding a new test user

```bash
# Authenticate to kcadm.sh
docker exec keycloak /opt/jboss/keycloak/bin/kcadm.sh config credentials \
  --server http://localhost:8080/auth \
  --realm master --user admin --password admin

# Create the user
docker exec keycloak /opt/jboss/keycloak/bin/kcadm.sh create users \
  -r Development \
  -s username=<USERNAME> \
  -s email=<USERNAME>@example.test \
  -s enabled=true \
  -s emailVerified=true \
  -s firstName=Ops -s lastName=<USERNAME-UPPER>

# Set the password (omit --temporary so the user doesn't have to reset on first login)
docker exec keycloak /opt/jboss/keycloak/bin/kcadm.sh set-password \
  -r Development --username <USERNAME> --new-password <PASSWORD>
```

After provisioning, add the new credentials to the deployment's `.env`
file (NOT this doc) under a `TEST_USER_<N>_*` pair so the harness can
pick them up.

## Verifying a user can authenticate

Direct-grant flow (bypasses the browser, useful for CI smoke tests):

```bash
curl -s -X POST \
  "https://<keycloak-host>/auth/realms/Development/protocol/openid-connect/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "grant_type=password&client_id=subscription-service-ui&client_secret=<SECRET>&username=<USERNAME>&password=<PASSWORD>" \
  | jq '{access_token_len: (.access_token|length), token_type, error, error_description}'
```

A successful response shows `access_token_len > 0` and no `error` /
`error_description`.

Full browser auth-code flow: visit
`https://<ui-host>/signin` and click "Sign in with OIDC". You should
land on `/dashboard` with the username visible in the top bar.

## Multi-user isolation acceptance test

The exact procedure required by ticket #423 acceptance criterion #3:

1. In browser context A, sign in as `opsa`. Confirm `/dashboard`
   shows the opsa user.
2. In a SEPARATE browser context (incognito or a different browser),
   sign in as `opsb`. Confirm `/dashboard` shows opsb.
3. In context A, sign out (click "Sign out"). Confirm context A is
   redirected to `/signin`.
4. In context B, refresh `/dashboard`. Confirm opsb is STILL signed in.

The maintainer's curl-based regression of this is in the git log for
ticket #423; ticket #424 will codify it as a Playwright e2e test.
