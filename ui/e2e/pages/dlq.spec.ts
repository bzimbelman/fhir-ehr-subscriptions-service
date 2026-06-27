import { expect, test } from "@playwright/test";

/**
 * /dlq smoke test. Header is `<h1>Dead-letter queue</h1>` (see DlqView.tsx).
 */
test("dlq page renders", async ({ page }) => {
  await page.goto("/dlq");
  await expect(page).toHaveURL(/\/dlq$/);

  await expect(
    page.getByRole("heading", { name: /dead-letter queue/i, level: 1 }),
  ).toBeVisible();

  await expect(
    page.getByRole("navigation", { name: /primary/i }),
  ).toBeVisible();
  await expect(page.locator("main").first()).toBeVisible();
});
