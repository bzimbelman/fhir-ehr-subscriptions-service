import { expect, test } from "@playwright/test";
import { getTestCredentials, loginAsUser } from "../helpers/keycloak-login";

/**
 * Multi-user session isolation.
 *
 * Two independent browser contexts -> two distinct Keycloak users
 * (TEST_USER_A and TEST_USER_B). After both have signed in we assert:
 *   1. Each context's /dashboard shows ITS user's identity in the
 *      `data-testid="topbar-username"` element (not the other user's).
 *   2. Signing user A out (via the Sign out button) does NOT affect user B
 *      -- B can still navigate to /dashboard without being bounced back to
 *      /signin, and the username on B's page still matches B.
 *
 * The whole point: cookies / NextAuth sessions are isolated per browser
 * context. A multi-tenant deployment where one operator's logout nuked the
 * other operator's session would be broken; this test pins that down.
 */
test("two users have isolated sessions", async ({ browser }) => {
  const a = getTestCredentials("A");
  const b = getTestCredentials("B");
  test.skip(
    a.username === b.username,
    "TEST_USER_A and TEST_USER_B must be distinct accounts; configure two " +
      "Keycloak users in the Development realm before running this test.",
  );

  // --- User A ---------------------------------------------------------
  const contextA = await browser.newContext();
  const pageA = await contextA.newPage();
  await loginAsUser(pageA, a.username, a.password);
  await expect(pageA).toHaveURL(/\/dashboard/);
  await expect(pageA.getByTestId("topbar-username")).toHaveText(a.username);

  // --- User B ---------------------------------------------------------
  const contextB = await browser.newContext();
  const pageB = await contextB.newPage();
  await loginAsUser(pageB, b.username, b.password);
  await expect(pageB).toHaveURL(/\/dashboard/);
  await expect(pageB.getByTestId("topbar-username")).toHaveText(b.username);

  // Cross-check: each context still shows ITS user, not the other.
  await expect(pageA.getByTestId("topbar-username")).toHaveText(a.username);
  await expect(pageB.getByTestId("topbar-username")).toHaveText(b.username);

  // --- Sign A out, B must remain authenticated ------------------------
  await pageA
    .getByRole("button", { name: /sign out/i })
    .click();
  await pageA.waitForURL(/\/signin/);

  // B navigates to /dashboard -- no redirect to /signin, username intact.
  await pageB.goto("/dashboard");
  await expect(pageB).toHaveURL(/\/dashboard/);
  await expect(pageB.getByTestId("topbar-username")).toHaveText(b.username);

  await contextA.close();
  await contextB.close();
});
