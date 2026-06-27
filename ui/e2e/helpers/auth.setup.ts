import { test as setup } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";
import { getTestCredentials, loginAsUser } from "./keycloak-login";

/**
 * Auth setup: sign in once per test user, persist the resulting cookie jar
 * to disk. The per-page specs (`pages/*.spec.ts`) reuse this via the
 * `storageState` use option, so each page test does NOT pay the Keycloak
 * round-trip cost. The multi-user test does NOT use storageState -- it
 * creates fresh contexts so it can prove session isolation across users.
 */

const AUTH_DIR = path.join(__dirname, "..", "..", "playwright", ".auth");

setup.beforeAll(() => {
  fs.mkdirSync(AUTH_DIR, { recursive: true });
});

setup("authenticate as opsa", async ({ page }) => {
  const { username, password } = getTestCredentials("A");
  await loginAsUser(page, username, password);
  await page.context().storageState({
    path: path.join(AUTH_DIR, "opsa.json"),
  });
});
