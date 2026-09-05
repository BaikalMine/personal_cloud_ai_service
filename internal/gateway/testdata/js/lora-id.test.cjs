const assert = require("node:assert/strict");
const { test } = require("node:test");
const { readFileSync } = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { createID } = require("../../static/lora-dataset-state.js");

test("dataset ID uses native UUID when available", () => {
  const native = { randomUUID: () => "native-uuid", getRandomValues: () => { throw new Error("unexpected fallback"); } };
  assert.equal(createID(native), "native-uuid");
});

test("HTTP dataset ID retains 128 random bits without randomUUID", () => {
  let calls = 0;
  const random = { getRandomValues: bytes => {
    assert.equal(bytes.length, 16);
    calls++;
    bytes.forEach((_, i) => { bytes[i] = (calls - 1) * 16 + i; });
    return bytes;
  } };
  assert.equal(createID(random), "000102030405060708090a0b0c0d0e0f");
  assert.equal(createID(random), "101112131415161718191a1b1c1d1e1f");
  assert.equal(calls, 2);
});

test("dataset and caption modules initialize on HTTP without UUID support", () => {
  let calls = 0;
  const sandbox = { crypto: { getRandomValues: bytes => { bytes.fill(++calls); return bytes; } }, setTimeout, clearTimeout };
  sandbox.window = sandbox;
  vm.createContext(sandbox);
  for (const file of ["lora-dataset-state.js", "lora-caption-state.js"]) {
    vm.runInContext(readFileSync(path.join(__dirname, "../../static", file), "utf8"), sandbox);
  }
  const controller = sandbox.AIGatewayLoraDataset.createController({ request: async () => ({}), defaults: { images: [] } });
  assert.equal(controller.state.status, "loading");
  const item = { id: "image", asset_id: "asset", caption: "", caption_revision: "" };
  const settings = { trigger_word: "subject", concept_type: "character" };
  const job = { job_id: "job", image_id: "image", state: "completed", caption: "subject, daylight", source: { image: { ...item }, ...settings } };
  const manifest = { settings, images: [item] };
  assert.equal(sandbox.AIGatewayLoraCaptions.reconcile(manifest, [job]), 1);
  assert.match(item.caption_revision, /^[0-9a-f]{32}$/);
  assert.equal(calls, 2);
  controller.dispose();
});
