import { expect, test } from "@playwright/test";

/**
 * /dashboard smoke test.
 *
 * The dashboard's top bar is the most stable on-page landmark for the page
 * identity assertion: it renders the "subscription-service" name and the
 * `data-testid="dashboard-top-bar"` element (see DashboardView.tsx). The
 * page does NOT have an `<h1>` element; it uses an `<h2 id="stats-heading">`
 * with the text "At a glance" for the first section.
 */
test("dashboard page renders", async ({ page }) => {
  await page.goto("/dashboard");
  await expect(page).toHaveURL(/\/dashboard$/);

  // Top-bar identifies this as the dashboard
  await expect(page.getByTestId("dashboard-top-bar")).toBeVisible();
  await expect(
    page.getByRole("heading", { name: /at a glance/i }),
  ).toBeVisible();

  // Layout landmarks
  await expect(
    page.getByRole("navigation", { name: /primary/i }),
  ).toBeVisible();
  await expect(page.locator("main").first()).toBeVisible();
});
