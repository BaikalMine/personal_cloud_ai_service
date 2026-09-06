const { test, expect, firefox, webkit } = require("@playwright/test");
const path = require("node:path");
const { settlePage, assertNoViewportOverflow } = require("./helpers.cjs");
for (const [browserName, engine] of [["firefox", firefox], ["webkit", webkit]]) {
  test(`${browserName} dialogs preserve applied prompts and trigger-first captions`, async ({ baseURL }, info) => {
    test.skip(info.project.name !== "desktop-1440", "browser-engine smoke uses the canonical viewport");
    const browser = await engine.launch();
    const context = await browser.newContext({ baseURL, viewport: { width: 1440, height: 900 }, locale: "ru-RU", colorScheme: "dark", reducedMotion: "reduce" });
    const page = await context.newPage();
    try {
    const errors = []; page.on("pageerror", error => errors.push(error.message));
    page.on("dialog", dialog => dialog.type() === "beforeunload" ? dialog.accept() : dialog.dismiss());
    await page.goto("/preview/generate"); await settlePage(page);
    await page.locator("[data-theme-preference-control]").first().selectOption("light");
    await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
    await page.locator("#positive-prompt").fill("A ceramic bowl in daylight.");
    await page.locator("#prompt-assistant-open").click();
    await page.locator("#prompt-assistant-improve").click();
    await expect(page.locator("#prompt-assistant-apply")).toBeEnabled();
    await page.locator("#prompt-assistant-draft").fill("A blue ceramic bowl on a table.");
    await page.locator("#prompt-assistant-apply").click();
    await expect(page.locator("#positive-prompt")).toHaveValue("A blue ceramic bowl on a table.");
    await expect(page.locator("#prompt-assistant-open")).toBeFocused();
    await expect(page.locator("#generation-draft-status")).toHaveText("Сохранено");
    await page.goto("/preview/lora-training"); await settlePage(page);
    await page.locator("[data-theme-preference-control]").first().selectOption("dark");
    await page.locator('[name="name"]').fill("Browser review");
    await page.locator('[name="trigger_word"]').fill("person_x");
    await page.locator("[data-lora-images]").setInputFiles(path.join(__dirname, "../../docs/frontend/prototype/assets/portrait.jpg"));
    await expect(page.locator("[data-dataset-save-state]")).toHaveAttribute("data-state", "saved");
    await page.locator("[data-caption-open]").click();
    await page.locator("[data-caption-image]").evaluate(image => image.decode());
    await page.locator("[data-caption-text]").fill("Blue coat, window light.");
    await page.locator("[data-caption-save]").click();
    await expect(page.locator("[data-caption-save-state]")).toHaveText("Сохранено");
    await assertNoViewportOverflow(page, "caption engine smoke");
    await page.screenshot({ path: info.outputPath(`caption-${browserName}.png`) });
    await page.keyboard.press("Escape");
    await expect(page.locator("[data-caption-open]")).toBeFocused();
    await expect(page.locator(".lora-dataset-item textarea")).toHaveValue("person_x, Blue coat, window light.");
    expect(errors).toEqual([]);
    } finally { await browser.close(); }
  });
}
