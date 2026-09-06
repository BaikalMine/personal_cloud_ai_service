const { test, expect } = require("@playwright/test");
const AxeBuilder = require("@axe-core/playwright").default;
const path = require("node:path");
const { settlePage, assertNoViewportOverflow, expectFocusInside } = require("./helpers.cjs");

async function open(page, theme = "dark", route = "/preview/generate") {
  await page.context().clearCookies({ name: "preview_generation_draft" });
  await page.addInitScript(value => localStorage.setItem("ai_gateway_theme", value), theme);
  await page.goto(route);
  await settlePage(page);
  await page.locator("#positive-prompt").fill("A ceramic vase on a table, soft daylight.");
  await page.locator("#prompt-assistant-open").click();
}
async function request(page) {
  await page.locator("#prompt-assistant-improve").click();
  await expect(page.locator("#prompt-assistant-review")).toBeVisible();
  await expect(page.locator("#prompt-assistant-apply")).toBeEnabled();
}
test.beforeEach(async ({ page }) => page.on("dialog", d => d.type() === "beforeunload" ? d.accept() : d.dismiss()));

test("assistant panel fits both themes, focuses correctly and applies only on request", async ({ page }, info) => {
  for (const theme of ["dark", "light"]) {
    await open(page, theme);
    await expectFocusInside(page, "#prompt-assistant");
    await request(page);
    await expect(page.locator("#positive-prompt")).toHaveValue("A ceramic vase on a table, soft daylight.");
    await expect(page.locator("#prompt-assistant-diff")).not.toHaveAttribute("open", "");
    await page.locator("#prompt-assistant-edit").click();
    await expect(page.locator("#prompt-assistant-draft")).toBeFocused();
    await page.locator("#prompt-assistant-draft").fill("My edited ceramic vase, soft warm light.");
    await page.locator("#prompt-assistant-improve").click();
    await expect(page.locator("#prompt-assistant-draft")).toHaveValue("My edited ceramic vase, soft warm light.");
    await page.locator("#prompt-assistant-diff > summary").click();
    await expect(page.locator("#prompt-assistant-diff-original")).toHaveText("A ceramic vase on a table, soft daylight.");
    await page.locator("#prompt-assistant-diff > summary").click();
    await page.locator(".assistant-panel-body").evaluate(el => el.scrollTo(0, 0));
    await expect(page.locator("#prompt-assistant-apply")).toBeInViewport();
    await assertNoViewportOverflow(page, "assistant panel");
    const axe = await new AxeBuilder({ page }).include("#prompt-assistant").analyze();
    expect(axe.violations.filter(v => ["serious", "critical"].includes(v.impact))).toEqual([]);
    await page.screenshot({ path: info.outputPath(`assistant-${theme}.png`) });
    await page.keyboard.press("Escape");
    await expect(page.locator("#prompt-assistant-open")).toBeFocused();
    await page.locator("#prompt-assistant-open").click();
    await expect(page.locator("#prompt-assistant-draft")).toHaveValue("My edited ceramic vase, soft warm light.");
    await page.locator("#prompt-assistant-apply").click();
    await expect(page.locator("#prompt-assistant")).not.toBeVisible();
    await expect(page.locator("#positive-prompt")).toHaveValue("My edited ceramic vase, soft warm light.");
    await page.locator("#prompt-assistant-open").click();
    await expect(page.locator("#prompt-assistant-review")).toBeVisible();
    await expect(page.locator("#prompt-assistant-draft")).toHaveValue("My edited ceramic vase, soft warm light.");
  }
});

test("source corrections are scoped and stale manual drafts survive model changes and reload", async ({ page }, info) => {
  await open(page, "light", "/preview/generate?template=image-to-image&workflow=photoflow-flux2-edit&media=1&slot=1&role=identity");
  await expect(page.locator("#prompt-assistant-source-list img")).toHaveCount(1);
  await request(page);
  await page.locator(".assistant-correction > summary").click();
  await page.locator('[data-assistant-correction="Picture 1"]').fill("Light hair, not dark.");
  await expect(page.locator("#prompt-assistant-stale")).toBeVisible();
  const sent = page.waitForRequest(r => r.url().endsWith("/generate/prompt-assistant") && r.method() === "POST");
  await request(page);
  expect(new URLSearchParams((await sent).postData()).get("image_correction_1")).toBe("Light hair, not dark.");
  await page.locator("#prompt-assistant-draft").fill("Keep this manually edited proposal.");
  await page.keyboard.press("Escape");
  await page.locator('[data-preset-id="photoflow-krea2-edit"]').click();
  await page.locator("#prompt-assistant-open").click();
  await expect(page.locator("#prompt-assistant-stale")).toBeVisible();
  await expect(page.locator("#prompt-assistant-apply")).toBeDisabled();
  await expect(page.locator("#prompt-assistant-draft")).toHaveValue("Keep this manually edited proposal.");
  await page.screenshot({ path: info.outputPath("assistant-stale.png") });
  await page.keyboard.press("Escape");
  await page.locator(".studio-saved > summary").click();
  await page.locator("#generation-draft-save").click();
  await expect(page.locator("#generation-draft-status")).toHaveText("Сохранено");
  await page.goto("/preview/generate");
  await settlePage(page);
  await page.locator("#prompt-assistant-open").click();
  await expect(page.locator("#prompt-assistant-stale")).toBeVisible();
  await expect(page.locator("#prompt-assistant-draft")).toHaveValue("Keep this manually edited proposal.");
});

test("late response cannot overwrite a changed prompt or steal focus from a closed panel", async ({ page }) => {
  let release;
  const pending = new Promise(resolve => { release = resolve; });
  let arrived;
  const started = new Promise(resolve => { arrived = resolve; });
  await page.route("**/generate/prompt-assistant", async route => {
    arrived();
    await pending;
    await route.fulfill({ json: { prompt: "Late answer", correlation_id: "late", references: [] } });
  });
  await open(page);
  await page.locator("#prompt-assistant-improve").click();
  await started;
  await page.keyboard.press("Escape");
  await page.locator("#positive-prompt").fill("A new unrelated request.");
  release();
  await expect(page.locator("#prompt-assistant-improve")).toBeEnabled();
  await expect(page.locator("#positive-prompt")).toBeFocused();
  await page.locator("#prompt-assistant-open").click();
  await expect(page.locator("#prompt-assistant-apply")).toBeDisabled();
  await expect(page.locator("#prompt-assistant-draft")).not.toHaveValue("Late answer");
  await page.locator("#prompt-assistant-keep").click();
  await expect(page.locator("#positive-prompt")).toHaveValue("A new unrelated request.");
});

test("reference replacement never inherits the previous photo correction", async ({ page }, info) => {
  await open(page, "dark", "/preview/generate?template=image-to-image&workflow=photoflow-flux2-edit&media=1&slot=1&role=identity");
  await request(page);
  await page.locator(".assistant-correction > summary").click();
  await page.locator('[data-assistant-correction="Picture 1"]').fill("This photo has a red coat.");
  await page.keyboard.press("Escape");
  await page.locator("#source-image").setInputFiles(path.join(__dirname, "../../docs/frontend/prototype/assets/portrait.jpg"));
  await page.locator("#prompt-assistant-open").click();
  const sent = page.waitForRequest(r => r.url().endsWith("/generate/prompt-assistant") && r.method() === "POST");
  await request(page);
  expect(new URLSearchParams((await sent).postData()).has("image_correction_1")).toBe(false);
  await page.locator(".assistant-correction > summary").click();
  await expect(page.locator('[data-assistant-correction="Picture 1"]')).toHaveValue("");
  await page.locator(".assistant-correction > summary").click();
  await page.locator(".assistant-panel-body").evaluate(el => el.scrollTo(0, 0));
  await page.screenshot({ path: info.outputPath("assistant-photo.png") });
});

test("assistant error can be retried and automatic video profiles follow exact frames", async ({ page }) => {
  let attempts = 0;
  await page.route("**/generate/prompt-assistant", route => {
    attempts++;
    return attempts === 1 ? route.fulfill({ status: 504, json: { error: "Сервис временно недоступен" } }) : route.continue();
  });
  await open(page);
  await page.locator("#prompt-assistant-improve").click();
  await expect(page.locator("#prompt-assistant-state")).toContainText("Сервис временно недоступен");
  await page.keyboard.press("Escape");
  await page.locator("#prompt-assistant-open").click();
  await expect(page.locator("#prompt-assistant-state")).toContainText("Сервис временно недоступен");
  await expect(page.locator("#prompt-assistant-apply")).toBeDisabled();
  await request(page);
  await page.keyboard.press("Escape");
  await page.locator('[data-workflow-id="minimax-h3-video"]').click();
  await page.locator("#prompt-assistant-open").click();
  await expect(page.locator("#prompt-assistant-mode")).toContainText("T2VA");
  await expect(page.locator("#prompt-assistant-template-field")).not.toBeVisible();
  await expect(page.locator("#prompt-assistant-template")).toHaveValue("minimax-h3-fl2va");
  await page.keyboard.press("Escape");
  await page.locator("#source-image").setInputFiles(path.join(__dirname, "../../docs/frontend/prototype/assets/portrait.jpg"));
  await page.locator("#prompt-assistant-open").click();
  await expect(page.locator("#prompt-assistant-mode")).toContainText("I2VA");
  await page.keyboard.press("Escape");
  await page.locator('#minimax-video-mode input[value="references"]').check();
  await page.locator("#prompt-assistant-open").click();
  await expect(page.locator("#prompt-assistant-mode")).toContainText("REF2VA");
  await expect(page.locator("#prompt-assistant-template")).toHaveValue("minimax-h3-ref2va");
});
