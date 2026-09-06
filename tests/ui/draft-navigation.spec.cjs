const { test, expect } = require("@playwright/test");
const { settlePage, assertNoViewportOverflow } = require("./helpers.cjs");

test("failed save exposes draft recovery outside the account overlay and mobile result view", async ({ page }) => {
  await page.goto("/preview/generate"); await settlePage(page);
  page.on("dialog", dialog => dialog.dismiss());
  await page.route("**/generate/draft", route => route.request().method() === "POST"
    ? route.fulfill({ status: 503, json: { error: "Не удалось сохранить тестовый черновик" } }) : route.continue());
  await page.locator("#positive-prompt").fill("Keep the complete draft after a failed save");
  if (page.viewportSize().width < 900) await page.locator("#studio-result-tab").click();
  await page.locator("[data-account-toggle]").click();
  await page.locator('#workspace-account a[href="/account/profile"]').click();
  await expect(page).toHaveURL(/\/preview\/generate$/);
  await expect(page.locator("#workspace-account")).toBeHidden();
  await expect(page.locator("#generation-draft-status")).toHaveText("Не удалось сохранить тестовый черновик");
  await expect(page.locator("#generation-draft-save")).toBeFocused();
  await expect(page.locator("#generation-draft-save")).toBeVisible();
  await expect(page.locator("#positive-prompt")).toHaveValue("Keep the complete draft after a failed save");
  await assertNoViewportOverflow(page, "draft navigation recovery");
  await page.unroute("**/generate/draft");
  await page.locator("#generation-draft-save").click();
  await expect(page.locator("#generation-draft-status")).toHaveText("Сохранено");
});
