import { expect, test } from "@playwright/test";

/**
 * /audit smoke test. Header is `<h1>Audit log</h1>` (see AuditView.tsx).
 */
test("audit page renders", async ({ page }) => {
  await page.goto("/audit");
  await expect(page).toHaveURL(/\/audit$/);

  await expect(
    page.getByRole("heading", { name: /^audit log$/i, level: 1 }),
  ).toBeVisible();

  await expect(
    page.getByRole("navigation", { name: /primary/i }),
  ).toBeVisible();
  await expect(page.locator("main").first()).toBeVisible();
});
