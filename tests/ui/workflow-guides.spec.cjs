const { test, expect } = require("@playwright/test");
const { settlePage, assertNoViewportOverflow } = require("./helpers.cjs");

test("workflow guides match Krea text and actual video assistant inputs", async ({ page }, info) => {
  await page.goto("/preview/generate");
  await settlePage(page);
  await page.locator(".studio-help > summary").click();
  const textGuide = page.locator('[data-workflow-guide="text"]');
  await textGuide.locator("summary").click();
  await expect(textGuide).toContainText("Для Krea2 по тексту негативный промпт не нужен");
  await expect(page.locator("#generation-negative-prompt-field")).toBeHidden();
  await assertNoViewportOverflow(page, "Krea text guide");
  await page.locator('[data-workflow-id="minimax-h3-video"]').click();
  const videoGuide = page.locator('[data-workflow-guide="video"]');
  await videoGuide.locator("summary").click();
  await expect(videoGuide).toContainText("Он анализирует выбранные фото");
  await expect(videoGuide).toContainText("содержимое этих файлов ассистент не анализирует");
  await assertNoViewportOverflow(page, "video guide");
  await videoGuide.locator("section").filter({ hasText: "содержимое этих файлов ассистент не анализирует" }).scrollIntoViewIfNeeded();
  await page.screenshot({ path: info.outputPath("video-guide.png") });
});
