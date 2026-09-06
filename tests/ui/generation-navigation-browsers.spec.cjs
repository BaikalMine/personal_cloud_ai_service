const { test, expect, chromium, firefox, webkit } = require("@playwright/test");
const { settlePage } = require("./helpers.cjs");
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; };

for (const [name, engine] of [["chromium", chromium], ["firefox", firefox], ["webkit", webkit]]) {
  test(`${name} internal navigation saves the latest edit before leaving the studio`, async ({ baseURL }, info) => {
    test.skip(info.project.name !== "desktop-1440", "browser-engine navigation uses the canonical viewport");
    const browser = await engine.launch();
    const context = await browser.newContext({ baseURL, viewport: { width: 1440, height: 900 } });
    const page = await context.newPage(), gate = deferred(), started = deferred();
    const errors = [], dialogs = [];
    page.on("pageerror", error => errors.push(error.message));
    page.on("dialog", async dialog => { dialogs.push(dialog.type()); await dialog.dismiss(); });
    try {
      await page.goto("/preview/generate"); await settlePage(page);
      let held = false;
      await page.route("**/generate/draft", async route => {
        if (route.request().method() === "POST" && !held) { held = true; started.resolve(); await gate.promise; }
        await route.continue();
      });
      await page.route("**/train-lora", async route => route.fulfill({ response: await context.request.get("/preview/lora-training") }));
      await page.locator("#positive-prompt").fill("First autosave");
      await started.promise;
      await page.locator("#positive-prompt").fill("Latest edit before opening LoRA");
      await page.locator('#workspace-navigation a[href="/train-lora"]').click();
      await expect(page).toHaveURL(/\/preview\/generate$/);
      expect(dialogs).toEqual([]);
      gate.resolve();
      await expect(page).toHaveURL(/\/train-lora$/);
      await page.waitForLoadState("load");
      await settlePage(page);
      const response = await context.request.get("/generate/draft");
      expect((await response.json()).draft.values.positive_prompt).toBe("Latest edit before opening LoRA");
      await page.goto("/preview/generate"); await settlePage(page);
      await expect(page.locator("#positive-prompt")).toHaveValue("Latest edit before opening LoRA");
      expect(errors).toEqual([]);
    } finally { gate.resolve(); await browser.close(); }
  });

  test(`${name} failed navigation save retains the prompt and exposes retry`, async ({ baseURL }, info) => {
    test.skip(info.project.name !== "desktop-1440", "browser-engine navigation uses the canonical viewport");
    const browser = await engine.launch();
    const context = await browser.newContext({ baseURL, viewport: { width: 1440, height: 900 } });
    const page = await context.newPage(), errors = [];
    page.on("pageerror", error => errors.push(error.message));
    page.on("dialog", dialog => dialog.dismiss());
    try {
      await page.goto("/preview/generate"); await settlePage(page);
      await page.route("**/generate/draft", route => route.request().method() === "POST"
        ? route.fulfill({ status: 503, json: { error: "Сервис временно недоступен" } }) : route.continue());
      await page.locator("#positive-prompt").fill("Keep this prompt on the page");
      await page.locator('#workspace-navigation a[href="/train-lora"]').click();
      await expect(page).toHaveURL(/\/preview\/generate$/);
      await expect(page.locator("#generation-draft-status")).toHaveText("Сервис временно недоступен");
      await expect(page.locator("#generation-draft-save")).toBeFocused();
      await expect(page.locator("#positive-prompt")).toHaveValue("Keep this prompt on the page");
      await page.unroute("**/generate/draft");
      await page.locator("#generation-draft-save").click();
      await expect(page.locator("#generation-draft-status")).toHaveText("Сохранено");
      expect(errors).toEqual([]);
    } finally { await browser.close(); }
  });
}
