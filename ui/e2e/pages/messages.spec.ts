import { expect, test } from "@playwright/test";

/**
 * /messages smoke test. Header is `<h1>Messages</h1>` (see
 * MessagesListView.tsx).
 */
test("messages page renders", async ({ page }) => {
  await page.goto("/messages");
  await expect(page).toHaveURL(/\/messages$/);

  await expect(
    page.getByRole("heading", { name: /^messages$/i, level: 1 }),
  ).toBeVisible();

  await expect(
    page.getByRole("navigation", { name: /primary/i }),
  ).toBeVisible();
  await expect(page.locator("main").first()).toBeVisible();
});
