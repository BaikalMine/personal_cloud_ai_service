const { test, expect } = require("@playwright/test");
const { installPreviewClock, settlePage, chooseWorkflow } = require("./helpers.cjs");

const cases = [
  { id: "krea-text", scenario: "text-to-image", preset: "photoflow-krea2", images: 0 },
  { id: "krea-edit", scenario: "image-to-image", preset: "photoflow-krea2-edit", images: 2 },
  { id: "flux-edit", scenario: "image-to-image", preset: "photoflow-flux2-edit", images: 4 },
  { id: "minimax-frames", scenario: "minimax-h3-video", preset: "minimax-h3-video", mode: "frames", images: 2 },
  { id: "minimax-references", scenario: "minimax-h3-video", preset: "minimax-h3-video", mode: "references", images: 4 },
];
const canonical = body => JSON.stringify([...body].filter(([name]) => !["csrf", "draft_revision"].includes(name)).sort(([left], [right]) => left.localeCompare(right)), null, 2);

for (const example of cases) {
  test(`launch and draft payload contract: ${example.id}`, async ({ page, browserName }, info) => {
    test.skip(browserName !== "chromium" || info.project.name !== "desktop-1440", "The wire-format golden is independent of viewport and browser.");
    page.on("dialog", dialog => dialog.type() === "beforeunload" ? dialog.accept() : dialog.dismiss());
    await installPreviewClock(page);
    let launched, saved;
    await page.route("**/generate/preflight", route => {
      launched = new URLSearchParams(route.request().postData());
      return route.fulfill({ json: { ok: true, checks: [] } });
    });
    page.on("request", request => {
      if (new URL(request.url()).pathname === "/generate/draft" && request.method() === "POST") saved = new URLSearchParams(request.postData());
    });
    await page.goto("/preview/generate");
    await settlePage(page);
    await page.locator(`[data-workflow-id="${example.scenario}"]`).click();
    await chooseWorkflow(page, example.preset);
    if (example.mode) await page.locator(`[name="video_mode"][value="${example.mode}"]`).check();
    for (let index = 1; index <= example.images; index++) {
      await page.locator(`[data-image-slot="${index}"] [data-gallery-image-picker-open]`).click();
      await page.getByRole("button", { name: "Выбрать AI-Gateway-Krea2-product.png", exact: true }).click();
    }
    await page.locator("#positive-prompt").fill("A precise reference scene with controlled light and preserved identity.");
    await page.locator("#generation-preflight-button").click();
    await expect.poll(() => Boolean(launched)).toBe(true);
    expect(canonical(launched)).toMatchSnapshot(`${example.id}-launch.txt`);
    await page.locator(".studio-saved > summary").click();
    for (let pass = 0; pass < 2; pass++) {
      await page.locator("#generation-draft-save").click();
      await expect(page.locator("#generation-draft-bar")).toHaveAttribute("data-state", "saved");
    }
    expect(canonical(saved)).toMatchSnapshot(`${example.id}-draft.txt`);
  });
}
