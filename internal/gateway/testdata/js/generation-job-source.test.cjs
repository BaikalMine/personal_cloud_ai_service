const test = require("node:test");
const assert = require("node:assert/strict");
const { createController, validID } = require("../../static/generation-job-source.js");
const payload = () => ({ parent_job_id: "source-1", values: { template_id: "image", generation_workflow: "workflow", model: "model", positive_prompt: "Original" }, requires_inputs: false });
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; };

test("job source only loads parameters and never executes a generation", async () => {
  const calls = []; let applied, consumed = 0;
  const controller = createController({ id: "source-1", load: async (id, signal) => { calls.push(id); assert.equal(signal.aborted, false); return payload(); }, apply: value => { applied = value; }, onConsumed: () => consumed++ });
  assert.equal(controller.snapshot().blocked, true); assert.equal(calls.length, 0);
  await controller.start();
  assert.deepEqual(calls, ["source-1"]); assert.deepEqual(applied, payload());
  assert.equal(controller.snapshot().status, "ready"); assert.equal(controller.snapshot().blocked, false); assert.equal(consumed, 1);
  await controller.start(); assert.equal(calls.length, 1);
});

test("late source data waits for explicit replacement after local edits", async () => {
  const hold = deferred(); let applied = 0;
  const controller = createController({ id: "source-1", load: () => hold.promise, apply: () => applied++ });
  const pending = controller.start(); controller.markEdited(); hold.resolve(payload()); await pending;
  assert.equal(controller.snapshot().status, "conflict"); assert.equal(controller.snapshot().blocked, true); assert.equal(applied, 0);
  controller.confirmReplace(); assert.equal(applied, 1); assert.equal(controller.snapshot().blocked, false);
  controller.confirmReplace(); assert.equal(applied, 1);
});

test("keeping local work aborts source loading and ignores its late reply", async () => {
  const hold = deferred(); let signal, applied = 0;
  const controller = createController({ id: "source-1", load: (_, s) => { signal = s; return hold.promise; }, apply: () => applied++ });
  const pending = controller.start(); controller.keepCurrent();
  assert.equal(signal.aborted, true); hold.resolve(payload()); await pending;
  assert.equal(applied, 0); assert.equal(controller.snapshot().status, "kept"); assert.equal(controller.snapshot().blocked, false);
});

test("page pause cancels a read and resumes the exact source without replaying a stale reply", async () => {
  const first = deferred(), second = deferred(); const calls = []; let applied = 0;
  const controller = createController({ id: "source-1", load: (id, signal) => { calls.push({ id, signal }); return calls.length === 1 ? first.promise : second.promise; }, apply: () => applied++ });
  const pending = controller.start(); controller.setActive(false);
  assert.equal(controller.snapshot().status, "paused"); assert.equal(calls[0].signal.aborted, true);
  controller.setActive(true); first.resolve(payload()); await pending; assert.equal(applied, 0);
  second.resolve(payload()); await new Promise(resolve => setImmediate(resolve));
  assert.equal(applied, 1); assert.deepEqual(calls.map(call => call.id), ["source-1", "source-1"]);
  controller.setActive(false); controller.setActive(true); assert.equal(calls.length, 2);
});

test("invalid, mismatched and incomplete source parameters fail closed", async () => {
  assert.equal(validID("../source"), false); assert.equal(validID(""), false);
  assert.equal(validID("x".repeat(201)), false);
  for (const data of [{ ...payload(), parent_job_id: "another" }, { ...payload(), values: [] }, { ...payload(), values: {} }, { ...payload(), values: { ...payload().values, model: 3 } }]) {
    let applied = 0;
    const controller = createController({ id: "source-1", load: async () => data, apply: () => applied++ });
    await controller.start(); assert.equal(controller.snapshot().status, "error"); assert.equal(controller.snapshot().blocked, true); assert.equal(applied, 0);
  }
});

test("source failures retry the same ID and unavailable controls remain blocked", async () => {
  const calls = []; let failed = true, available = false, applied = 0;
  const controller = createController({ id: "source-1", load: async id => { calls.push(id); if (failed) throw new TypeError("Failed to fetch"); return payload(); }, apply: () => { if (!available) throw new Error("Model unavailable"); applied++; } });
  await controller.start(); assert.match(controller.snapshot().error, /соединение/);
  failed = false; await controller.start(); assert.equal(controller.snapshot().status, "error"); assert.equal(applied, 0);
  available = true; await controller.start(); assert.equal(applied, 1); assert.deepEqual(calls, ["source-1", "source-1", "source-1"]);
});
test("LoRA source has a separate identity and never accepts generation lineage", async () => {
  const good = { source_kind: "lora_training", source_id: "lora-1", values: { template_id: "text-to-image", generation_workflow: "recipe", model: "model", loras_configured: "true", lora_1: "Exact/adapter.safetensors", lora_model_strength_1: "1", lora_clip_strength_1: "1" } };
  for (const payload of [good, { ...good, parent_job_id: "lora-1" }, { ...good, source_id: "other" }, { ...good, source_kind: "generation" }, { ...good, values: { ...good.values, parent_job_id: "lora-1" } },
    ...[{ lora_1: "" }, { loras_configured: "false" }, { lora_model_strength_1: "NaN" }, { lora_clip_strength_1: "" }].map(values => ({ ...good, values: { ...good.values, ...values } }))]) {
    let applied = 0;
    const controller = createController({ id: "lora-1", kind: "lora_training", load: async () => payload, apply: () => applied++ });
    await controller.start();
    assert.equal(controller.snapshot().status, payload === good ? "ready" : "error");
    assert.equal(applied, payload === good ? 1 : 0);
  }
});

test("explicit empty source stays blocked until kept", async () => {
  const controller = createController({ id: "", enabled: true, load: async () => { throw new Error("must not load"); }, apply: () => assert.fail("must not apply") });
  await controller.start(); assert.equal(controller.snapshot().status, "error");
  controller.keepCurrent(); assert.equal(controller.snapshot().blocked, false);
});
