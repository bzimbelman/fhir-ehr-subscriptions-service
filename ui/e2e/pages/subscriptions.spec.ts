import { expect, test } from "@playwright/test";

/**
 * /subscriptions smoke test. Header is `<h1>Subscriptions</h1>` (see
 * SubscriptionsList.tsx).
 */
test("subscriptions page renders", async ({ page }) => {
  await page.goto("/subscriptions");
  await expect(page).toHaveURL(/\/subscriptions$/);

  await expect(
    page.getByRole("heading", { name: /^subscriptions$/i, level: 1 }),
  ).toBeVisible();

  await expect(
    page.getByRole("navigation", { name: /primary/i }),
  ).toBeVisible();
  await expect(page.locator("main").first()).toBeVisible();
});
