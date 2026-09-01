const path = require("node:path");
const { test, expect } = require("@playwright/test");
const AxeBuilder = require("@axe-core/playwright").default;
const { settlePage, assertNoViewportOverflow, expectFocusInside } = require("./helpers.cjs");

const visualStyle = path.join(__dirname, "visual-stability.css");
const responsiveRoutes = [
  { route: "/preview/generate", snapshot: "generation-step-1.png" },
  { route: "/preview/gallery", snapshot: "gallery.png" },
  { route: "/preview/invites", snapshot: "invitations.png" },
  { route: "/preview/users", snapshot: "users.png" },
  { route: "/preview/admin", snapshot: "admin-dashboard.png" },
  { route: "/preview/content", snapshot: "ai-content.png" },
];

const open = async (page, route) => {
  await page.goto(route, { waitUntil: "domcontentloaded" });
  await settlePage(page);
};

const desktopOnly = (testInfo) => {
  test.skip(testInfo.project.name !== "desktop-1440", "interaction and axe checks run once in the canonical desktop project");
};

test("key surfaces fit every supported viewport", async ({ page }, testInfo) => {
  for (const { route, snapshot } of responsiveRoutes) {
    await open(page, route);
    await assertNoViewportOverflow(page, `${testInfo.project.name} ${route}`);
    await expect(page).toHaveScreenshot(snapshot, { fullPage: true, stylePath: visualStyle });
  }
});

test("generation wizard covers Krea2, Flux2 and MiniMax", async ({ page }, testInfo) => {
  desktopOnly(testInfo);

  await open(page, "/preview/generate");
  await page.getByRole("button", { name: /Текст в изображение/ }).click();
  await expect(page.locator(".generation-workflow-choice.is-selected")).toContainText("PhotoFlow Krea2");
  await expect(page.locator("#generation-model")).toHaveValue("krea2:test");
  await page.getByRole("button", { name: "Продолжить" }).click();
  await expect(page.locator("#generation-summary")).toContainText("Текст в изображение");

  await open(page, "/preview/generate");
  await page.getByRole("button", { name: /Фото и промт/ }).click();
  await page.getByRole("button", { name: /Flux2 Редактирование/ }).click();
  await expect(page.locator("#generation-model")).toHaveValue("flux2:test");
  const primaryGallery = page.locator('[data-image-slot="1"] [data-gallery-image-picker-open]');
  await primaryGallery.click();
  await page.getByRole("button", { name: "Выбрать AI-Gateway-Krea2-portrait.png" }).click();
  await expect(page.locator('[data-image-slot="1"] [data-image-name]')).toHaveText("AI-Gateway-Krea2-portrait.png");
  await page.locator("#workflow-next").click();
  await expect(page.locator("#generation-summary")).toContainText("Flux 2 / Klein 9B");
  await expect(page.locator("#generation-summary")).toContainText("1 фото (1 из галереи)");
  const promptBox = await page.locator("#positive-prompt").boundingBox();
  const launchBox = await page.locator("#generation-run-dock").boundingBox();
  expect(launchBox.y, "launch actions must stay below the prompt instead of covering it").toBeGreaterThan(promptBox.y + promptBox.height);
  await expect(page).toHaveScreenshot("generation-flux-summary.png", { fullPage: true, stylePath: visualStyle });

  await open(page, "/preview/generate");
  await page.getByRole("button", { name: /Видео Создаёт/ }).click();
  await expect(page.locator("#generation-mode-guide-title")).toHaveText("Видео полностью по вашему описанию");
  await page.locator('[data-image-slot="1"] [data-gallery-image-picker-open]').click();
  await page.getByRole("button", { name: "Выбрать AI-Gateway-Krea2-portrait.png" }).click();
  await expect(page.locator("#generation-mode-guide-title")).toHaveText("Видео начинается с выбранного фото");
  await page.locator('[data-image-slot="2"] [data-gallery-image-picker-open]').click();
  await page.getByRole("button", { name: "Выбрать AI-Gateway-Krea2-product.png" }).click();
  await expect(page.locator("#generation-mode-guide-title")).toHaveText("Переход между двумя точными кадрами");
  await page.locator("#minimax-video-mode label").filter({ hasText: "Промт + свободные референсы" }).click();
  await expect(page.locator("#generation-mode-guide-title")).toHaveText("Видео по промту и выбранным ориентирам");
  await expect(page.locator(".source-image-card:visible")).toHaveCount(4);
  await expect(page.locator("#minimax-audio-reference")).toBeVisible();
  await expect(page.locator("#minimax-video-reference")).toBeVisible();
});

test("wizard, image picker and lightbox work from the keyboard", async ({ page }, testInfo) => {
  desktopOnly(testInfo);

  await open(page, "/preview/generate");
  const imageScenario = page.getByRole("button", { name: /Фото и промт/ });
  await imageScenario.focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("heading", { name: "Выберите workflow и модель" })).toBeVisible();
  const fluxWorkflow = page.getByRole("button", { name: /Flux2 Редактирование/ });
  await fluxWorkflow.focus();
  await page.keyboard.press("Enter");
  const pickerTrigger = page.locator('[data-image-slot="1"] [data-gallery-image-picker-open]');
  await pickerTrigger.focus();
  await page.keyboard.press("Enter");
  await expect(page.locator("#generation-image-picker")).toBeVisible();
  await expectFocusInside(page, "#generation-image-picker");
  await page.keyboard.press("Escape");
  await expect(page.locator("#generation-image-picker")).toBeHidden();
  await expect(pickerTrigger).toBeFocused();

  await open(page, "/preview/gallery");
  const mediaTrigger = page.locator("[data-gallery-open]").first();
  await mediaTrigger.focus();
  await page.keyboard.press("Enter");
  await expect(page.locator("#gallery-lightbox")).toBeVisible();
  await expectFocusInside(page, "#gallery-lightbox");
  await page.keyboard.press("Tab");
  await expectFocusInside(page, "#gallery-lightbox");
  await page.keyboard.press("Escape");
  await expect(page.locator("#gallery-lightbox")).toBeHidden();
  await expect(mediaTrigger).toBeFocused();
});

test("preview exposes loading, empty, error, sensitive, offline, queued and completed states", async ({ page }, testInfo) => {
  desktopOnly(testInfo);

  await open(page, "/preview/components");
  await expect(page.locator(".ui-status-banner.is-loading")).toBeVisible();
  await expect(page.locator(".ui-status-banner.is-error")).toBeVisible();

  await open(page, "/preview/gallery-empty");
  await expect(page.locator(".ui-empty-state, .empty-state")).toBeVisible();

  await open(page, "/preview/gallery");
  await expect(page.locator("[data-sensitive-media]")).toHaveCount(1);
  await expect(page.getByText("Контент 18+")).toBeVisible();

  await open(page, "/preview/admin");
  await expect(page.locator(".dependency-state.offline").first()).toContainText("Нет связи");

  await open(page, "/preview/generate");
  await expect(page.locator("#generation-variant-list")).toContainText("В очереди");
  await expect(page.locator("#generation-variant-list")).toContainText("Готово");
  await expect(page.locator("#generation-variant-list")).toContainText("Ошибка");
});

test("critical product surfaces have no serious axe violations", async ({ page }, testInfo) => {
  desktopOnly(testInfo);
  const routes = ["/preview/components", "/preview/generate", "/preview/gallery", "/preview/invites", "/preview/users"];
  const routeViolations = [];

  for (const route of routes) {
    await open(page, route);
    const result = await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
      .analyze();
    const violations = result.violations
      .filter((entry) => entry.impact === "critical" || entry.impact === "serious")
      .map((entry) => ({
        id: entry.id,
        impact: entry.impact,
        help: entry.help,
        targets: entry.nodes.map((node) => node.target.join(" ")),
      }));
    if (violations.length) routeViolations.push({ route, violations });
  }
  expect(routeViolations, JSON.stringify(routeViolations, null, 2)).toEqual([]);
});
