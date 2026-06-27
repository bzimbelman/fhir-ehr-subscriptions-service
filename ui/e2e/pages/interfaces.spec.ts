import { expect, test } from "@playwright/test";

/**
 * /interfaces smoke test.
 *
 * Asserts the page header (`<h1>Interfaces</h1>`) plus the primary nav and
 * main landmark are present. Data may or may not be loaded -- this test is
 * intentionally indifferent to whether there are any rows.
 */
test("interfaces page renders", async ({ page }) => {
  await page.goto("/interfaces");
  await expect(page).toHaveURL(/\/interfaces$/);

  await expect(
    page.getByRole("heading", { name: /^interfaces$/i, level: 1 }),
  ).toBeVisible();

  await expect(
    page.getByRole("navigation", { name: /primary/i }),
  ).toBeVisible();
  await expect(page.locator("main").first()).toBeVisible();
});
