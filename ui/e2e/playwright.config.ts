import { defineConfig, devices } from "@playwright/test";
import path from "node:path";

/**
 * Playwright config for subscription-service operator UI (Epic #398, ticket
 * #424).
 *
 * The same test bodies run against:
 *   - local docker-compose stack:  http://localhost:3000   (default)
 *   - public reference deployment: https://subscription-service-ui.bzonfhir.com
 *
 * Select the target by exporting `PLAYWRIGHT_BASE_URL`. The default suits a
 * developer who's just run `docker compose up -d`.
 *
 * Auth is performed once per user via `helpers/auth.setup.ts`, which writes a
 * `storageState` JSON file under `playwright/.auth/<username>.json`. The
 * per-page tests then reuse that cookie jar so each spec doesn't pay the
 * Keycloak round-trip cost.
 */

const BASE_URL =
  process.env.PLAYWRIGHT_BASE_URL ?? "http://localhost:3000";

const STORAGE_DIR = path.join(__dirname, "..", "playwright", ".auth");

export default defineConfig({
  testDir: ".",
  // No global retries: tests should be deterministic. If a test flakes, fix
  // the test, don't paper over it with retries.
  retries: 0,
  // Single worker keeps the Keycloak login flow stable -- the public IdP
  // doesn't love concurrent auth round-trips from the same client.
  workers: 1,
  fullyParallel: false,
  reporter: [["list"]],
  timeout: 60_000,
  expect: {
    timeout: 10_000,
  },
  use: {
    baseURL: BASE_URL,
    headless: true,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: "off",
    // Be slightly generous with action timeouts -- the OIDC redirect chain
    // can take a beat on first run when Keycloak warms up.
    actionTimeout: 15_000,
    navigationTimeout: 30_000,
  },
  projects: [
    {
      name: "setup",
      testMatch: /helpers\/auth\.setup\.ts$/,
      use: { ...devices["Desktop Chrome"] },
    },
    {
      name: "as-opsa",
      dependencies: ["setup"],
      testMatch: /pages\/.+\.spec\.ts$/,
      use: {
        ...devices["Desktop Chrome"],
        storageState: path.join(STORAGE_DIR, "opsa.json"),
      },
    },
    {
      name: "multi-user",
      dependencies: ["setup"],
      testMatch: /multi-user\/.+\.spec\.ts$/,
      use: { ...devices["Desktop Chrome"] },
    },
  ],
});
