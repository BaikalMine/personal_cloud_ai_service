const path = require("node:path");
const fs = require("node:fs");
const { test, expect } = require("@playwright/test");
const { installPreviewClock, settlePage, assertNoViewportOverflow } = require("./helpers.cjs");

const screenshotStyle = fs.readFileSync(path.join(__dirname, "component-visual-stability.css"), "utf8");

async function open(page, route, theme) {
  await page.context().clearCookies({ name: "preview_generation_draft" });
  await page.context().clearCookies({ name: "preview_lora_datasets" });
  await installPreviewClock(page);
  await page.addInitScript(value => localStorage.setItem("ai_gateway_theme", value), theme);
  await page.goto(route, { waitUntil: "domcontentloaded" });
  await settlePage(page);
  await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
}

async function alignedFields(page, context) {
  const report = await page.locator(".ui-field-grid:visible").evaluateAll(grids => grids.flatMap(grid => {
    const rows = new Map();
    for (const field of grid.querySelectorAll(":scope > .ui-field")) {
      const control = field.querySelector(":scope > input, :scope > select, :scope > textarea");
      const box = field.getBoundingClientRect();
      if (!control || box.width < 1 || box.height < 1) continue;
      const controlBox = control.getBoundingClientRect();
      if (controlBox.width < 1 || controlBox.height < 1) continue;
      const rowKey = Math.round(box.y);
      const row = rows.get(rowKey) || [];
      row.push({ name: control.name, top: controlBox.y, height: controlBox.height, tag: control.tagName });
      rows.set(rowKey, row);
    }
    return [...rows.values()].map(fields => ({
      grid: grid.className,
      fields,
      offset: Math.max(...fields.map(field => field.top)) - Math.min(...fields.map(field => field.top)),
    }));
  }));
  expect(report.length, `${context}: visible field groups`).toBeGreaterThan(0);
  for (const row of report) {
    expect.soft(row.offset, `${context}: ${row.grid}: ${row.fields.map(field => field.name).join(", ")}`).toBeLessThanOrEqual(1);
    for (const field of row.fields.filter(field => field.tag !== "TEXTAREA")) {
      expect.soft(field.height, `${context}: ${field.name} control height`).toBeGreaterThanOrEqual(44);
    }
  }
  const loraOffsets = await page.locator(".lora-row:visible").evaluateAll(rows => rows.map(row => {
    const number = row.querySelector(".lora-number").getBoundingClientRect();
    const control = row.querySelector("select").getBoundingClientRect();
    return Math.abs(number.y + number.height / 2 - control.y - control.height / 2);
  }));
  expect.soft(loraOffsets.filter(offset => offset > 1), `${context}: LoRA numbers align with their controls`).toEqual([]);
}

test.beforeEach(async ({ page }) => {
  page.on("dialog", dialog => dialog.type() === "beforeunload" ? dialog.accept() : dialog.dismiss());
});

test("shared fields align wrapped labels and messages in both themes", async ({ page }, testInfo) => {
  for (const theme of ["dark", "light"]) {
    await open(page, "/preview/components", theme);
    await alignedFields(page, `${theme} component states`);
    const firstControl = page.locator('[name="example_seed"]');
    const firstTop = (await firstControl.boundingBox()).y;
    await page.locator("#example-duration-error").evaluate(node => {
      node.textContent = `${node.textContent} `.repeat(4);
    });
    await alignedFields(page, `${theme} long validation message`);
    expect((await firstControl.boundingBox()).y).toBe(firstTop);
    await expect(page.locator('[name="example_model"]')).toBeDisabled();
    await expect(page.locator('[name="example_resolution"]')).toHaveAttribute("readonly", "");
    await firstControl.focus();
    await page.keyboard.press("Tab");
    await expect(page.locator('[name="example_scheduler"]')).toBeFocused();
    await page.keyboard.press("Tab");
    await expect(page.locator('[name="example_duration"]')).toBeFocused();
    await page.keyboard.press("Tab");
    await expect(page.locator('[name="example_resolution"]')).toBeFocused();
    const disclosure = page.locator("[data-control-disclosure]");
    await disclosure.locator("summary").focus();
    await page.keyboard.press("Enter");
    await expect(disclosure).toHaveAttribute("open", "");
    await page.locator('[name="example_postprocess"]').check();
    await expect(page.locator('[name="example_postprocess"]')).toBeChecked();
    const fontSize = await firstControl.evaluate(node => parseFloat(getComputedStyle(node).fontSize));
    expect(fontSize).toBe(page.viewportSize().width < 760 ? 16 : 14);
    await assertNoViewportOverflow(page, `${theme} controls`);
    await page.locator(".component-field-matrix").screenshot({ path: testInfo.outputPath(`controls-${theme}.png`), style: screenshotStyle });
  }
});

const workflows = [
  { id: "krea-text", scenario: /Текст в изображение/ },
  { id: "krea-edit", scenario: /Фото и промт/, preset: /Krea 2: редактирование/, image: true },
  { id: "flux-edit", scenario: /Фото и промт/, preset: /Flux2 Редактирование/, image: true },
  { id: "minimax-frames", scenario: /Видео Создаёт/ },
  { id: "minimax-references", scenario: /Видео Создаёт/, references: true },
];

for (const workflow of workflows) {
  test(`${workflow.id} uses aligned controls in both themes`, async ({ page }, testInfo) => {
    test.setTimeout(90_000);
    for (const theme of ["dark", "light"]) {
      await open(page, "/preview/generate", theme);
      await page.getByRole("button", { name: workflow.scenario }).click();
      if (workflow.preset) await page.getByRole("button", { name: workflow.preset }).click();
      if (workflow.references) {
        await page.locator("#minimax-video-mode label").filter({ hasText: "Промт + свободные референсы" }).click();
      }
      if (workflow.image) {
        await page.locator('[data-image-slot="1"] [data-gallery-image-picker-open]').click();
        await page.getByRole("button", { name: "Выбрать AI-Gateway-Krea2-portrait.png" }).click();
      }
      await page.locator("#workflow-next").click();
      await page.locator("#generation-open-exact").click();
      if (workflow.image) {
        const lut = page.locator('[data-lut-enabled]:visible');
        await expect(lut).not.toBeChecked();
        await lut.check();
        await expect(page.locator('[data-lut-strength]:visible')).toBeEnabled();
      }
      await settlePage(page);
      await alignedFields(page, `${theme} ${workflow.id}`);
      await assertNoViewportOverflow(page, `${theme} ${workflow.id}`);
      await page.locator(".generation-advanced").screenshot({ path: testInfo.outputPath(`${workflow.id}-${theme}.png`), style: screenshotStyle });
    }
  });
}

test("account and administration field grids stay aligned", async ({ page }) => {
  test.setTimeout(90_000);
  for (const route of ["/preview/lora-training", "/preview/invites", "/preview/user"]) {
    for (const theme of ["dark", "light"]) {
      await open(page, route, theme);
      await alignedFields(page, `${theme} ${route}`);
      await assertNoViewportOverflow(page, `${theme} ${route}`);
    }
  }
});
