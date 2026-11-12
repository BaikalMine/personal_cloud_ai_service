const assert = require("node:assert/strict");
const test = require("node:test");
const output = require("../../static/generation-output.js");
const video = require("../../static/generation-video.js");
const batch = require("../../static/generation-batch.js");
const fixtures = require("../generation_dimensions.json");

test("Full Ultra reports VOSR2 size and percentage outpaint accurately",()=>{
  const values={generation_workflow:'krea2-pro',aspect_ratio:'custom',width:880,height:1168,krea_pro_upscale:2,krea_pro_mode:'text'};
  let result=output.plan({family:'krea2',templateID:'text-to-image',values});
  assert.deepEqual(result.final,{width:1760,height:2336});
  result=output.plan({family:'krea2',templateID:'text-to-image',values:{...values,krea_pro_mode:'outpaint'}});
  assert.deepEqual(result.base,{width:1056,height:1168});
  assert.deepEqual(result.final,{width:2112,height:2336});
  result=output.plan({family:'krea2',templateID:'text-to-image',values:{...values,krea_pro_settings:'{"enabled":{"4046":false}}'}});
  assert.deepEqual(result.final,{width:880,height:1168});
});

test("V29 final size follows the exclusive upscaler and Relay scale", () => {
  const values = {generation_workflow:'krea2-pro',aspect_ratio:'custom',width:1216,height:1616,krea_pro_upscale:3,krea_pro_settings:'{"enabled":{"4046":true,"4150":true}}'};
  for (const [upscaler,relayScale,scale] of [['none',4,1],['vosr',4,3],['relay',2,2],['relay',4,4]]) {
    const result = output.plan({family:'krea2',templateID:'text-to-image',values:{...values,krea_pro_upscaler:upscaler,krea_pro_relay_scale:relayScale}});
    assert.deepEqual(result.final,{width:1216*scale,height:1616*scale});
    assert.match(output.facts([result]).compact,new RegExp(`${1216*scale} × ${1616*scale}`));
    assert.ok(!result.processing.includes('VOSR2 3×') || upscaler==='vosr');
  }
});

test("V29 outpaint is expanded before Relay and oversized frames have no expected output", () => {
  const values={generation_workflow:'krea2-pro',aspect_ratio:'custom',width:880,height:1168,krea_pro_mode:'outpaint',krea_pro_upscaler:'relay',krea_pro_relay_scale:4};
  const result=output.plan({family:'krea2',templateID:'text-to-image',values});
  assert.deepEqual(result.base,{width:1056,height:1168});
  assert.deepEqual(result.final,{width:4224,height:4672});
  const oversized=output.plan({family:'krea2',templateID:'text-to-image',values:{...values,krea_pro_settings:'{"nodes":{"3870":{"left":400,"right":400}}}'}});
  assert.equal(oversized.final,null);
  assert.ok(oversized.error);
  assert.ok(output.facts([oversized]).compact.includes(oversized.error));
});

test("V29 summary enforces Python's 12288-side and 38.43 MP final-output profile", () => {
  for (const [width,height,mode,valid] of [[3072,256,'text',true],[3088,256,'text',false],[1584,1584,'text',true],[1600,1584,'text',false],[1312,1584,'outpaint',true],[1328,1584,'outpaint',false]]) {
    const values={generation_workflow:'krea2-pro',aspect_ratio:'custom',width,height,krea_pro_mode:mode,krea_pro_upscaler:'relay',krea_pro_relay_scale:4};
    const result=output.plan({family:'krea2',templateID:'text-to-image',values});
    assert.equal(Boolean(result.final),valid,`${width}x${height} ${mode}`);
    if (!valid) assert.ok(output.facts([result]).compact.includes(result.error));
  }
});

test("Krea2 Ultra reports native resolution without the PhotoFlow upscale", () => {
  const result = output.plan({ family: "krea2", templateID: "text-to-image", values: {
    generation_workflow: "krea2-ultra", width: 1296, height: 1728, base_megapixels: 1,
    krea_ultra_speed_boost: true,
  } });
  assert.deepEqual(result.base, result.final);
  assert.ok(result.processing.includes("Speed Boost"));
  assert.ok(!result.processing.includes("Апскейл Krea2"));
});

test("DLSS FG increases only AIHUB video FPS and retains duration and geometry", () => {
  for (const mode of ["photo", "video", "carousel"]) {
    for (const multiplier of [2, 3, 4]) {
      const input = { family: "minimax_h3", videoSize: { width: 512, height: 768 }, values: {
        generation_workflow: "aihub-minimax-h3", aihub_mode: mode, aihub_fg_multiplier: multiplier,
        aihub_match_duration: true, aihub_reference_duration: 10, video_duration_seconds: 10,
      } };
      const before = output.plan(input);
      input.values.aihub_fg_enabled = true;
      const after = output.plan(input);
      assert.equal(after.fps, mode === "carousel" ? 24 : 24 * multiplier);
      assert.equal(after.duration, before.duration);
      assert.deepEqual(after.final, before.final);
      input.values.generation_workflow = "minimax-h3-video";
      assert.equal(output.plan(input).fps, 24);
    }
  }
});

test("AIHUB reference length reports frame-accurate or unresolved duration", () => {
  for (const [duration, expected] of [[7.608667, 7.625], [11.666667, 280 / 24], [null, null]]) {
    const result = output.plan({ family: "minimax_h3", values: {
      generation_workflow: "aihub-minimax-h3", aihub_mode: "video", aihub_match_duration: true,
      aihub_reference_duration: duration, video_duration_seconds: 5,
    } });
    assert.equal(result.duration, expected);
    if (duration === null) assert.equal(output.facts([result], { video: true }).facts[2].value, "Как у видеореференса");
  }
});

test("AIHUB carousel counts selected frames and both upscale stages", () => {
  const result = output.plan({ family: "minimax_h3", videoSize: { width: 352, height: 640 }, values: {
    generation_workflow: "aihub-minimax-h3", aihub_mode: "carousel", aihub_photo_frames: 50,
    aihub_photo_stride: 8, aihub_photo_skip: 5, aihub_latent_scale: 2, video_rtx_enabled: true, video_rtx_scale: 2,
  } });
  assert.deepEqual(result.final, { width: 1408, height: 2560 });
  assert.equal(result.photoCountUpperBound, 6);
  assert.equal(output.facts([result]).compact, "до 6 фото · 1408 × 2560");
});

test("AIHUB video half resolution matches ties-to-even server alignment", () => {
  const result = output.plan({ family: "minimax_h3", videoSize: { width: 736, height: 1280 }, values: {
    generation_workflow: "aihub-minimax-h3", aihub_mode: "video", aihub_half_resolution: true,
    video_rtx_enabled: true, video_rtx_scale: 2, video_rife_enabled: true,
  } });
  assert.deepEqual(result.base, { width: 384, height: 640 });
  assert.deepEqual(result.final, { width: 768, height: 1280 });
  assert.equal(result.fps, 24);
});

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

test("AIHUB V2 keeps identical dimensions with either refiner", () => {
  for (const refiner of ['flux', 'qwen21']) {
    const result = output.plan({ family: 'minimax_h3', videoSize: { width: 352, height: 480 }, values: {
      generation_workflow: 'aihub-minimax-h3', aihub_mode: 'carousel', aihub_carousel_pipeline: 'v2',
      aihub_carousel_refiner: refiner, aihub_photo_count: 2,
    } });
    assert.deepEqual(result.final, { width: 352, height: 480 });
    assert.equal(result.photoCountUpperBound, 2);
    assert.deepEqual(result.processing, [refiner === 'qwen21' ? 'Qwen 2.1' : 'FLUX.2']);
  }
});
