import { expect, type Page } from "@playwright/test";

/**
 * Programmatic Keycloak sign-in helper.
 *
 * The operator UI's /signin page renders a single "Sign in with OIDC" button.
 * Clicking it kicks off the NextAuth.js redirect chain to Keycloak. Keycloak
 * shows its standard login form (username + password). On success the IdP
 * redirects back through /api/auth/callback/oidc and lands the user on
 * /dashboard.
 *
 * This helper drives that flow via normal Playwright page navigation -- no
 * direct HTTP/Curl-style token exchange. That keeps the test honest: it
 * exercises the same browser-side flow a real operator would use, including
 * cookie handling and the post-redirect session being readable by the Next.js
 * server-component `auth()` call on /dashboard.
 *
 * Auth.js sometimes inserts an intermediate "Continue to sign in" page on a
 * cold start (CSRF token bootstrap). We handle that defensively below.
 */
export async function loginAsUser(
  page: Page,
  username: string,
  password: string,
): Promise<void> {
  await page.goto("/signin");

  const signInButton = page.getByRole("button", {
    name: /sign in with oidc/i,
  });
  await expect(signInButton).toBeVisible();
  await signInButton.click();

  // Auth.js v5 occasionally serves an intermediate /api/auth/signin page
  // with a "Sign in with OIDC" button before the real Keycloak redirect.
  // If we land there, click it through.
  try {
    await page.waitForURL(
      (url) =>
        /\/realms\/[^/]+\/protocol\/openid-connect\/auth/.test(url.toString()) ||
        url.pathname.startsWith("/api/auth/signin"),
      { timeout: 15_000 },
    );
  } catch {
    // Some Auth.js builds redirect straight to Keycloak with no /api/auth
    // detour. That's fine -- fall through to the login form wait below.
  }

  if (page.url().includes("/api/auth/signin")) {
    const proceed = page.getByRole("button", { name: /sign in with oidc/i });
    if (await proceed.isVisible().catch(() => false)) {
      await proceed.click();
    }
  }

  // Wait for the Keycloak login form. The username field has name="username".
  const usernameField = page.locator('input[name="username"]');
  await expect(usernameField).toBeVisible({ timeout: 15_000 });
  await usernameField.fill(username);
  await page.locator('input[name="password"]').fill(password);

  // Keycloak's submit button is `<input name="login" type="submit">`. Newer
  // themes sometimes use `<button id="kc-login">`. Cover both.
  const loginButton = page
    .locator('input[name="login"], button[id="kc-login"], button[type="submit"]')
    .first();
  await loginButton.click();

  // Back on our UI's /dashboard once the OIDC callback completes.
  await page.waitForURL(/\/dashboard(\?.*)?$/, { timeout: 30_000 });
}

/**
 * Convenience: read the canonical test credentials from env. The CI / dev
 * harness exports these from the same .env that wires Keycloak realm
 * provisioning, so tests stay env-driven (never hard-code passwords).
 */
export function getTestCredentials(slot: "A" | "B"): {
  username: string;
  password: string;
} {
  const username = process.env[`TEST_USER_${slot}_USERNAME`];
  const password = process.env[`TEST_USER_${slot}_PASSWORD`];
  if (!username || !password) {
    throw new Error(
      `TEST_USER_${slot}_USERNAME / TEST_USER_${slot}_PASSWORD must be set ` +
        `to run the e2e suite. See ui/e2e/README.md.`,
    );
  }
  return { username, password };
}
