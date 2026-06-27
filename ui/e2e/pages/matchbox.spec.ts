import { expect, test } from "@playwright/test";

/**
 * /matchbox smoke test. Header is `<h1>Matchbox</h1>` (see MatchboxView.tsx).
 */
test("matchbox page renders", async ({ page }) => {
  await page.goto("/matchbox");
  await expect(page).toHaveURL(/\/matchbox$/);

  await expect(
    page.getByRole("heading", { name: /^matchbox$/i, level: 1 }),
  ).toBeVisible();

  await expect(
    page.getByRole("navigation", { name: /primary/i }),
  ).toBeVisible();
  await expect(page.locator("main").first()).toBeVisible();
});
