const test = require("node:test");
const assert = require("node:assert/strict");
const pp = require("../../static/postprocess-view.js");

const view = () => ({ version: 1, plan: { version: 1, media_type: "video", options: { width: 256, height: 160, interpolate: 2, rtx_quality: "HIGH", color_method: "adain", color_strength: 0.7 }, fps_numerator: 60000, fps_denominator: 1001, output_frames: 60 }, sources: [{ media_id: "9007199254740993", job_id: "source-job-00001", availability: "available", measurement: { width: 128, height: 80 } }] });
test("postprocessing never infers sampling from a missing seed", () => {
  assert.equal(pp.isJob({ operation: "postprocess" }), true);
  assert.equal(pp.isJob({ template_id: "postprocess-video-v1" }), true);
  assert.equal(pp.isJob({ seed: 0, template_id: "text-to-image" }), false);
  const facts = pp.facts(view());
  assert.ok(facts.some(([label, value]) => label === "Частота (задано), FPS" && value.includes("60000/1001")));
  assert.ok(facts.some(([label, value]) => label === "Размер исходника" && value === "128 × 80"));
  assert.ok(facts.some(([label, value]) => label === "Размер (задано)" && value === "256 × 160"));
  assert.ok(!facts.some(([label]) => /seed|sampler|модель/i.test(label)));
  assert.match(pp.summary(view()), /^План:/);
});
test("source links retain exact identities and require confirmed availability", () => {
  assert.equal(pp.sources(view())[0].href, "/gallery?media=9007199254740993");
  for (const availability of ["unavailable", "not_checked", "", undefined]) {
    const changed = view(); changed.sources[0].availability = availability;
    assert.equal(pp.sources(changed)[0].href, "");
  }
  const changed = view(); changed.sources[0].media_id = 9007199254740993;
  assert.equal(pp.sources(changed)[0].href, "");
});
test("missing plans are not fabricated from default sizes", () => {
  for (const input of [null, {}, { version: 2 }, { version: 1, plan: { options: { width: NaN, height: 128 } } }]) {
    assert.deepEqual(pp.facts(input), []);
    assert.deepEqual(pp.sources(input), []);
    assert.equal(pp.summary(input), "План обработки недоступен");
  }
});
test("requested output and measured file disagreement stays explicit", () => {
  const measured = { width: 256, height: 160, average_fps: "60000/1001" };
  assert.deepEqual(pp.differences(view(), measured), []);
  assert.equal(pp.differences(view(), { ...measured, width: 128 }).length, 1);
  assert.equal(pp.differences(view(), { ...measured, average_fps: "30/1" }).length, 1);
  assert.equal(pp.differences(view(), { width: 128, height: 80, average_fps: "30/1" }).length, 2);
  for (const invalid of ["30/0", "NaN", "-30/1", 30, "9999999999/1"]) assert.equal(pp.differences(view(), { ...measured, average_fps: invalid }).length, 0);
  assert.deepEqual(pp.differences(view(), null), []);
});
