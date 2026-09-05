const { test } = require("node:test");
const assert = require("node:assert/strict");
const { activeImageLimit, chooseModel, settingChanges, fragmentBounds, bindReferencePlayer } = require("../../static/generation-studio.js");
const { downloadURL } = require("../../static/generation-lightbox.js");

test("studio excludes every reference from text-only image generation", () => {
  assert.equal(activeImageLimit({ requiresImage: false, allowsImages: false, maximum: 4 }), 0);
});
test("studio uses workflow limits and distinguishes exact frames from references", () => {
  assert.equal(activeImageLimit({ requiresImage: true, maximum: 2 }), 2);
  assert.equal(activeImageLimit({ requiresImage: true, maximum: 4 }), 4);
  assert.equal(activeImageLimit({ allowsImages: true, maximum: 4, isVideo: true, videoMode: "frames" }), 2);
  assert.equal(activeImageLimit({ allowsImages: true, maximum: 4, isVideo: true, videoMode: "references" }), 4);
  assert.equal(activeImageLimit({ allowsImages: true, maximum: 10 }), 4);
});

test("model fallback stays within the available visible options", () => {
  const models = [{ value: "" }, { value: "wrong-family", hidden: true }, { value: "missing", disabled: true }, { value: "first" }, { value: "default" }];
  assert.equal(chooseModel(models, "default").value, "default");
  assert.equal(chooseModel(models, "missing").value, "first");
  assert.equal(chooseModel(models, "not-installed").value, "first");
  assert.equal(chooseModel(models, "").value, "first");
  assert.equal(chooseModel(models.slice(0, 3), "missing"), undefined);
});

test("profile feedback reports only actual changes with display labels", () => {
  const before = { steps: { label: "Steps", value: "8" }, seed: { label: "Seed", value: "-1" } };
  const after = { steps: { label: "Steps", value: "10" }, seed: before.seed, hidden: { value: "5" } };
  assert.deepEqual(settingChanges(before, after), [{ name: "steps", label: "Steps", before: "8", after: "10" }]);
});

test("fragment bounds clamp to source duration and accept negative audio offsets", () => {
  assert.deepEqual(fragmentBounds(3, 5, 6), { start: 3, end: 6 });
  assert.deepEqual(fragmentBounds("1,5", "2,5", 6), { start: 1.5, end: 4 });
  assert.deepEqual(fragmentBounds(-2, 5, 6), { start: 4, end: 6 });
  assert.deepEqual(fragmentBounds(-20, 5, 6), { start: 0, end: 5 });
  assert.deepEqual(fragmentBounds(20, 5, 6), { start: 6, end: 6 });
  assert.equal(fragmentBounds(0, 5, Infinity), null);
  assert.equal(fragmentBounds(0, 5, NaN), null);
  assert.equal(downloadURL("blob:https://example.com/abc"), "blob:https://example.com/abc");
});

test("reference player releases owned blobs, pauses fragments, and never rewrites inputs", async () => {
  const events = {};
  const revoked = [];
  const media = { duration: 10, currentTime: 0, paused: true, addEventListener: (name, cb) => { events[name] = cb; },
    pause() { this.paused = true; events.pause?.(); }, play() { this.paused = false; return Promise.resolve(); },
    load() {}, removeAttribute() { this.src = ""; }, getAttribute() { return this.src; } };
  const button = { addEventListener: (_, cb) => { button.click = cb; } };
  const status = {};
  const player = bindReferencePlayer({ media, button, status, start: () => 2, duration: () => 3,
    urlAPI: { createObjectURL: () => "blob:local", revokeObjectURL: value => revoked.push(value) } });
  player.setSource({});
  assert.equal(button.disabled, true);
  events.loadedmetadata();
  assert.equal(button.disabled, false);
  await button.click();
  assert.equal(media.currentTime, 2);
  assert.equal(media.paused, false);
  media.currentTime = 5;
  events.timeupdate();
  assert.equal(media.paused, true);
  player.setSource("/saved.wav");
  assert.deepEqual(revoked, ["blob:local"]);
  events.error();
  assert.equal(button.disabled, true);
  assert.ok(status.textContent);
  player.clear();
  assert.equal(status.textContent, "");
  assert.deepEqual(revoked, ["blob:local"]);
});
