import { expect, test } from "@playwright/test";

/**
 * /settings smoke test. Header is `<h1>Settings</h1>` (see SettingsView.tsx).
 */
test("settings page renders", async ({ page }) => {
  await page.goto("/settings");
  await expect(page).toHaveURL(/\/settings$/);

  await expect(
    page.getByRole("heading", { name: /^settings$/i, level: 1 }),
  ).toBeVisible();

  await expect(
    page.getByRole("navigation", { name: /primary/i }),
  ).toBeVisible();
  await expect(page.locator("main").first()).toBeVisible();
});
