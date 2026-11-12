const { test } = require("node:test");
const assert = require("node:assert/strict");
const { recommendations, createInspector, valid } = require("../../static/lora-dataset-quality.js");
const quality = (color = 100, low = false) => ({ version: 1, detail_score: low ? 1 : 100, low_detail: low, colors: Array(192).fill(color) });
const asset = (id, hash = id) => ({ id, sha256: hash, width: 768, height: 1024 });
const response = (id, q = quality()) => ({ asset_id: id, sha256: id, quality: q });
const image = (id, excluded = false) => ({ id: `frame-${id}`, asset_id: id, caption: "manual", excluded });
const deferred = () => { let resolve; const promise = new Promise(r => resolve = r); return { resolve, promise }; };

test("visual advice never mutates manual state and ignores excluded frames and exact duplicates in background vote", () => {
  const images = [image("a"), image("b"), image("c", true), image("d")];
  const assets = { a: asset("a"), b: asset("b"), c: asset("c"), d: asset("d", "a") };
  const results = new Map([["a:a", quality(100, true)], ["b:b", quality(103)], ["c:c", quality(104)], ["d:a", quality(100)]]);
  const before = JSON.stringify({ images, assets });
  const review = recommendations(images, assets, results);
  assert.equal(review.checked, 3); assert.equal(review.total, 3); assert.equal(review.background, false);
  assert.match(review.warnings.get("frame-a")[0], /резкость/);
  assert.match(review.warnings.get("frame-b")[0], /кадром 1/);
  assert.equal(review.warnings.has("frame-c"), false);
  assert.equal(JSON.stringify({ images, assets }), before);
});

test("background cue requires three distinct hashes and comparison uses current order", () => {
  const assets = Object.fromEntries(["a", "b", "c"].map(id => [id, asset(id)]));
  const results = new Map(Object.keys(assets).map(id => [`${id}:${id}`, quality()]));
  let review = recommendations([image("c"), image("a"), image("b")], assets, results);
  assert.equal(review.background, true); assert.match(review.warnings.get("frame-a")[0], /кадром 1/);
  results.set("c:c", quality(255));
  review = recommendations([image("a"), image("b"), image("c")], assets, results);
  assert.equal(review.background, false); assert.deepEqual(review.warnings.get("frame-c"), []);
  assets.b.width = 2000;
  assert.deepEqual(recommendations([image("a"), image("b")], assets, results).warnings.get("frame-b"), []);
});

test("unknown or malformed measurements never count as checked", () => {
  for (const bad of [{}, { ...quality(), version: 2 }, { ...quality(), colors: [10] }, { ...quality(), detail_score: Infinity }]) {
    assert.equal(valid(bad), false);
    const review = recommendations([image("a")], { a: asset("a") }, new Map([["a:a", bad]]));
    assert.equal(review.checked, 0);
  }
});

test("inspection is sequential, retry keeps completed measurements, and duplicate assets load once", async () => {
  const first = deferred(); const calls = []; let fail = true;
  const inspector = createInspector({ request: async id => { calls.push(id); if (id === "a") return first.promise; if (fail) throw new Error("temporary failure"); return response(id); } });
  const images = [image("a"), { ...image("a"), id: "another-frame" }, image("b"), image("c", true)];
  const assets = { a: asset("a"), b: asset("b"), c: asset("c") };
  const running = inspector.run(images, assets); await inspector.run(images, assets);
  assert.deepEqual(calls, ["a"]);
  first.resolve(response("a")); await running;
  assert.equal(inspector.state.processed, 1); assert.match(inspector.state.error, /failure/);
  fail = false; await inspector.run(images, assets);
  assert.deepEqual(calls, ["a", "b", "b"]); assert.equal(inspector.state.processed, 2); assert.equal(inspector.state.error, "");
});

test("cancel and dataset switch discard late responses without blocking a new run", async () => {
  const waits = [];
  const inspector = createInspector({ request: () => { const d = deferred(); waits.push(d); return d.promise; } });
  const images = [image("a")], assets = { a: asset("a") };
  let run = inspector.run(images, assets); inspector.cancel(); waits[0].resolve(response("a")); await run;
  assert.equal(inspector.state.cancelled, true); assert.equal(inspector.state.running, false); assert.equal(inspector.state.results.size, 0);
  run = inspector.run(images, assets); inspector.reset(); const newer = inspector.run(images, assets);
  waits[1].resolve(response("a")); await run; assert.equal(inspector.state.running, true); assert.equal(inspector.state.results.size, 0);
  waits[2].resolve(response("a")); await newer; assert.equal(inspector.state.results.size, 1);
});

test("mismatched response hash cannot attach a recommendation to another file", async () => {
  const inspector = createInspector({ request: async () => ({ ...response("a"), sha256: "other" }) });
  await inspector.run([image("a")], { a: asset("a") });
  assert.match(inspector.state.error, /неполный/); assert.equal(inspector.state.results.size, 0);
});

test("saved measurements restore without a new request and obsolete versions remain unchecked", async () => {
  let requests = 0;
  const inspector = createInspector({ request: async id => { requests++; return response(id); } });
  const assets = { a: { ...asset("a"), quality: quality() }, b: { ...asset("b"), quality: { ...quality(), version: 0 } } };
  inspector.reset(assets);
  assert.equal(inspector.state.hasRun, true); assert.equal(inspector.state.results.size, 1);
  assert.equal(recommendations([image("a"), image("b")], assets, inspector.state.results).checked, 1);
  await inspector.run([image("a"), image("b")], assets);
  assert.equal(requests, 1); assert.equal(inspector.state.results.size, 2);
});
