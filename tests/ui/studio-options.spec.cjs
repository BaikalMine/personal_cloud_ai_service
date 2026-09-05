const fs = require("node:fs");
const path = require("node:path");
const { test, expect } = require("@playwright/test");
const { settlePage, assertNoViewportOverflow } = require("./helpers.cjs");

async function open(page) {
  await page.goto("/preview/generate");
  await settlePage(page);
  await expect(page.locator("#generation-model")).not.toHaveValue("");
}
test.beforeEach(async ({ page }) => page.on("dialog", d => d.type() === "beforeunload" ? d.accept() : d.dismiss()));

test("primary image dimensions and profile feedback remain outside optional groups", async ({ page }) => {
  await open(page);
  await expect(page.locator('.studio-basic-settings [name="output_megapixels"]')).toBeVisible();
  await expect(page.locator('.studio-basic-settings [name="base_megapixels"]')).toBeVisible();
  const base = await page.locator('[name="base_megapixels"]').boundingBox();
  const output = await page.locator('[name="output_megapixels"]').boundingBox();
  expect(Math.abs(base.y - output.y)).toBeLessThanOrEqual(1);
  await page.locator('[data-generation-profile="maximum"]').click();
  await expect(page.locator("#studio-profile-changes")).toBeVisible();
  await page.locator("#studio-profile-changes > summary").click();
  await expect(page.locator("#studio-profile-changes")).toContainText("4.7");
  await page.locator('[name="output_megapixels"]').fill("2.5");
  await expect(page.locator("#studio-profile-changes")).toBeHidden();
  await expect(page.locator("[data-generation-profile].is-active")).toHaveCount(0);
  await assertNoViewportOverflow(page, "primary image dimensions");
});

test("closed purpose groups retain the same submitted values and independent Sage option", async ({ page }) => {
  await open(page);
  await page.locator('[data-studio-settings-target="lora"]').click();
  await expect(page.locator('[data-studio-option-group="lora"]')).toHaveAttribute("open", "");
  await expect(page.locator('[data-studio-option-group="processing"]')).not.toHaveAttribute("open", "");
  await page.locator('.krea-lora-row [name="lora_1"]').selectOption("Krea2-realism-V2.safetensors");
  await page.locator('.krea-lora-row [name="lora_model_strength_1"]').fill("0.65");
  await page.locator('[data-studio-option-group="memory"] > summary').click();
  await page.locator('[name="krea_sage_enabled"]').check();
  await page.locator('[data-studio-option-group="sampling"] > summary').click();
  await page.locator('[name="steps"]:enabled').fill("9");
  while (await page.locator('[data-studio-option-group][open]').count()) await page.locator('[data-studio-option-group][open] > summary').first().click();
  await page.keyboard.press("Escape");
  let body;
  await page.route("**/generate/preflight", route => {
    body = new URLSearchParams(route.request().postData());
    return route.fulfill({ json: { ok: true, checks: [] } });
  });
  await page.locator("#positive-prompt").fill("A glazed ceramic bowl on a wooden table.");
  await page.locator("#generation-preflight-button").click();
  await expect.poll(() => body?.get("steps")).toBe("9");
  expect(body.get("lora_1")).toBe("Krea2-realism-V2.safetensors");
  expect(body.get("lora_model_strength_1")).toBe("0.65");
  expect(body.get("krea_sage_enabled")).toBe("true");
  await page.locator('[data-studio-settings-target="processing"]').click();
  await expect(page.locator('[name="detail_enabled"]')).toBeChecked();
  await expect(page.locator('[name="color_transfer"]')).toBeChecked();
});

test("available alternative model is selected when workflow default is missing", async ({ page }) => {
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  page.on("console", message => { if (message.type() === "error") errors.push(message.text()); });
  await page.route("**/static/generate.js*", async route => {
    const response = await route.fetch();
    const body = 'document.querySelectorAll("[data-default-model-id]").forEach(el => { el.dataset.defaultModelId = "not-installed"; });\n' + await response.text();
    await route.fulfill({ response, body });
  });
  await page.goto("/preview/generate");
  await settlePage(page);
  expect(errors).toEqual([]);
  await expect(page.locator("#generation-model")).not.toHaveValue("");
  await expect(page.locator("#generation-model option:checked")).toHaveAttribute("data-family", "krea2");
  await expect(page.locator("#generation-model option:checked")).toHaveAttribute("data-available", "true");
});

test("reference image opens at full size and restores focus on close", async ({ page }) => {
  await open(page);
  await page.locator('[data-workflow-id="minimax-h3-video"]').click();
  const buffer = fs.readFileSync(path.join(__dirname, "../../docs/frontend/prototype/assets/portrait.jpg"));
  await page.locator("#source-image").setInputFiles({ name: "portrait.jpg", mimeType: "image/jpeg", buffer });
  const button = page.locator('[data-image-slot="1"] [data-image-preview-open]');
  await button.click();
  await expect(page.locator("#generation-lightbox")).toBeVisible();
  await expect(page.locator("#generation-lightbox-download")).toHaveAttribute("href", /^blob:/);
  await page.keyboard.press("Escape");
  await expect(button).toBeFocused();
});

function wav(seconds = 4) {
  const samples = 8000 * seconds;
  const buffer = Buffer.alloc(44 + samples * 2);
  buffer.write("RIFF"); buffer.writeUInt32LE(buffer.length - 8, 4); buffer.write("WAVEfmt ", 8);
  buffer.writeUInt32LE(16, 16); buffer.writeUInt16LE(1, 20); buffer.writeUInt16LE(1, 22);
  buffer.writeUInt32LE(8000, 24); buffer.writeUInt32LE(16000, 28); buffer.writeUInt16LE(2, 32); buffer.writeUInt16LE(16, 34);
  buffer.write("data", 36); buffer.writeUInt32LE(samples * 2, 40);
  return buffer;
}

test("video reference has native playback, fragment bounds and recoverable codec errors", async ({ page }, info) => {
  await open(page);
  const encoded = await page.evaluate(async () => {
    const canvas = document.createElement("canvas");
    canvas.width = 160; canvas.height = 120;
    const ctx = canvas.getContext("2d");
    const stream = canvas.captureStream(10);
    const recorder = new MediaRecorder(stream, { mimeType: "video/mp4" });
    const chunks = [];
    recorder.addEventListener("dataavailable", event => chunks.push(event.data));
    const done = new Promise(resolve => recorder.addEventListener("stop", resolve, { once: true }));
    recorder.start();
    for (let frame = 0; frame < 25; frame++) {
      ctx.fillStyle = frame % 2 ? "#287780" : "#dde2e4";
      ctx.fillRect(0, 0, 160, 120);
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    recorder.stop();
    await done;
    stream.getTracks().forEach(track => track.stop());
    const bytes = new Uint8Array(await new Blob(chunks, { type: "video/mp4" }).arrayBuffer());
    return btoa(String.fromCharCode(...bytes));
  });
  await page.locator('[data-workflow-id="minimax-h3-video"]').click();
  await page.locator('[name="video_mode"][value="references"]').check();
  await page.locator("#minimax-video-file").setInputFiles({ name: "motion.mp4", mimeType: "video/mp4", buffer: Buffer.from(encoded, "base64") });
  await expect(page.locator("#minimax-video-play")).toBeEnabled();
  await page.locator('[name="video_reference_start"]').fill("0.5");
  await page.locator('[name="video_reference_duration"]').fill("1");
  await page.locator("#minimax-video-play").click();
  await expect.poll(() => page.locator("#minimax-video-preview-media").evaluate(el => el.currentTime)).toBeGreaterThanOrEqual(0.5);
  await expect.poll(() => page.locator("#minimax-video-preview-media").evaluate(el => el.paused)).toBe(true);
  expect(await page.locator("#minimax-video-preview-media").evaluate(el => el.currentTime)).toBeLessThan(2);
  await assertNoViewportOverflow(page, "video player");
  expect((await page.locator("#minimax-video-name").boundingBox()).width).toBeGreaterThan(100);
  await page.screenshot({ path: info.outputPath("reference-video.png") });
  await page.locator("#minimax-video-file").setInputFiles({ name: "unsupported.mkv", mimeType: "video/x-matroska", buffer: Buffer.from("invalid") });
  await expect(page.locator("#minimax-video-playback-status")).toContainText("Браузер не воспроизводит");
  await expect(page.locator("#minimax-video-play")).toBeDisabled();
  await expect(page.locator("#minimax-video-name")).toHaveText("unsupported.mkv");
  await page.locator("#minimax-video-remove").click();
  await expect(page.locator("#minimax-video-preview")).toBeHidden();
});

test("audio reference plays selected offset and pauses when its mode is hidden", async ({ page }, info) => {
  await open(page);
  await page.locator('[data-workflow-id="minimax-h3-video"]').click();
  await page.locator('[name="video_mode"][value="references"]').check();
  await page.locator("#minimax-audio-file").setInputFiles({ name: "voice.wav", mimeType: "audio/wav", buffer: wav() });
  await expect(page.locator("#minimax-audio-play")).toBeEnabled();
  await page.locator('[name="video_audio_start"]').fill("1");
  await page.locator("#minimax-audio-play").click();
  await expect.poll(() => page.locator("#minimax-audio-preview-media").evaluate(el => el.currentTime)).toBeGreaterThanOrEqual(1);
  await expect.poll(() => page.locator("#minimax-audio-preview-media").evaluate(el => el.paused)).toBe(false);
  await assertNoViewportOverflow(page, "audio player");
  expect((await page.locator("#minimax-audio-name").boundingBox()).width).toBeGreaterThan(100);
  await page.screenshot({ path: info.outputPath("reference-audio.png") });
  await page.locator('[name="video_mode"][value="frames"]').check();
  await expect.poll(() => page.locator("#minimax-audio-preview-media").evaluate(el => el.paused)).toBe(true);
  await page.locator('[name="video_mode"][value="references"]').check();
  await expect(page.locator('[name="video_audio_start"]')).toHaveValue("1");
  await page.locator("#minimax-audio-remove").click();
  await expect(page.locator("#minimax-audio-preview-media")).not.toHaveAttribute("src", /.+/);
});
