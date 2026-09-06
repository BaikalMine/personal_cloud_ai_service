const assert = require("node:assert/strict");
const test = require("node:test");
const output = require("../../static/generation-output.js");
const video = require("../../static/generation-video.js");
const batch = require("../../static/generation-batch.js");
const fixtures = require("../generation_dimensions.json");

test("launch dimensions share golden fixtures with the Go workflow builder", () => {
  for (const f of fixtures) {
    const actual = f.kind === "video"
      ? video.scaledResolution({ sourceSize: { width: f.sourceWidth, height: f.sourceHeight }, maxResolution: f.quality })
      : output.imageResolution({ aspect: f.aspect, megapixels: f.megapixels, multiple: f.multiple, maxLongest: f.maxLongest });
    assert.equal(actual.width, f.width, f.name);
    assert.equal(actual.height, f.height, f.name);
    if (f.kind === "image") assert.deepEqual(output.baseResolution(actual, f.baseMP), { width: f.baseWidth, height: f.baseHeight });
  }
});

test("Krea text reports base and final pixels, not requested megapixels", () => {
  const result = output.plan({ family: "krea2", templateID: "text-to-image", values: { aspect_ratio: "3:4", output_megapixels: "1,9", base_megapixels: "1" } });
  assert.deepEqual(result.base, { width: 832, height: 1152 });
  assert.deepEqual(result.final, { width: 1216, height: 1616 });
});

test("RIFE and RTX are independent of base quality and inactive options", () => {
  const options = { family: "minimax_h3", videoSize: { width: 352, height: 480 }, values: { video_rife_multiplier: 4, video_rtx_scale: 2, video_duration_seconds: 15 } };
  assert.equal(output.plan(options).fps, 24);
  assert.deepEqual(output.plan(options).final, options.videoSize);
  const active = output.plan({ ...options, values: { ...options.values, video_rife_enabled: true, video_rtx_enabled: true } });
  assert.equal(active.fps, 96);
  assert.equal(active.duration, 15);
  assert.deepEqual(active.base, options.videoSize);
  assert.deepEqual(active.final, { width: 704, height: 960 });
  assert.equal(batch.parameterOptions({ family: "minimax_h3", rtx: true }).find(p => p.name === "video_rtx_scale").max, 2);
});

test("RTX fractional scale follows truncation and Python ties-to-even alignment", () => {
  const actual = output.plan({ family: "minimax_h3", videoSize: { width: 352, height: 480 }, values: { video_rtx_enabled: true, video_rtx_scale: "1,4" } });
  assert.deepEqual(actual.final, { width: 496, height: 672 });
  assert.equal(output.roundEven(10.5), 10);
  assert.equal(output.roundEven(11.5), 12);
});

const edit = { templateID: "image-to-image", sourceSize: { width: 1280, height: 1920 }, values: { width: 1280, height: 1920, edit_use_custom_size: true, max_longest_side: 4096 } };
test("Krea edit preserves size or applies the optional finishing scale", () => {
  const preserved = output.plan({ ...edit, family: "krea2", values: { ...edit.values, preserve_original_size: true, upscale_factor: 2 } });
  assert.deepEqual(preserved.base, edit.sourceSize);
  assert.deepEqual(preserved.final, edit.sourceSize);
  const scaled = output.plan({ ...edit, family: "krea2", values: { ...edit.values, upscale_factor: 1.5 } });
  assert.deepEqual(scaled.final, { width: 1920, height: 2880 });
  const capped = output.plan({ ...edit, family: "krea2", values: { ...edit.values, width: 4096, height: 4096, upscale_factor: 2 } });
  assert.deepEqual(capped.base, { width: 2216, height: 2216 });
  assert.deepEqual(capped.final, capped.base);
});

test("Flux reference megapixels do not change generation size", () => {
  for (const source_megapixels of [0.5, 1, 4]) {
    const plain = output.plan({ ...edit, family: "flux2", values: { ...edit.values, source_megapixels } });
    assert.deepEqual(plain.base, edit.sourceSize);
    assert.deepEqual(plain.final, edit.sourceSize);
    assert.equal(plain.referenceMP, source_megapixels);
  }
  assert.deepEqual(output.plan({ ...edit, family: "flux2", values: { ...edit.values, flux_upscale_mode: "ultimate" } }).final, { width: 1920, height: 2880 });
  const both = output.plan({ ...edit, family: "flux2", values: { ...edit.values, flux_upscale_mode: "both" } });
  assert.deepEqual(both.final, { width: 2732, height: 4098 });
  assert.equal(both.approximate, true);
});

test("Flux custom fit and latent alignment use source geometry", () => {
  const options = { ...edit, family: "flux2", values: { ...edit.values, width: 1000, height: 1000, edit_proportion: "resize" } };
  assert.deepEqual(output.plan(options).base, { width: 656, height: 992 });
  assert.deepEqual(output.plan({ ...options, values: { ...options.values, edit_proportion: "pad" } }).base, { width: 992, height: 992 });
  assert.deepEqual(output.plan({ family: "flux2", templateID: "text-to-image", values: { width: 1000, height: 1000, flux_upscale_mode: "both" } }).final, { width: 992, height: 992 });
});

test("missing source never claims the previous workflow dimensions", () => {
  const result = output.plan({ ...edit, family: "krea2", sourceSize: null });
  assert.equal(result.base, null);
  assert.equal(result.final, null);
  assert.match(output.facts([result]).compact, /После выбора фото/);
});

test("batch facts report count, varying duration and FPS", () => {
  const plans = [5, 10, 15].map(seconds => output.plan({ family: "minimax_h3", videoSize: { width: 320, height: 480 }, values: { video_duration_seconds: seconds } }));
  const result = output.facts(plans, { video: true, count: 3, variation: "Длительность видео: 5 → 15" });
  assert.equal(result.compact, "3 видео · 320 × 480");
  assert.equal(result.facts.find(f => f.label === "Длительность (задано)").value, "5 → 15 сек.");
  assert.equal(result.facts.find(f => f.label === "Частота кадров").value, "24 FPS");
});
