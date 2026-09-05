const { test } = require("node:test");
const assert = require("node:assert/strict");
const { activeImageLimit } = require("../../static/generation-studio.js");

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
