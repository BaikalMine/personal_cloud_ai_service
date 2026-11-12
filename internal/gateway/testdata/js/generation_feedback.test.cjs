const test = require("node:test");
const assert = require("node:assert/strict");
const feedback = require("../../static/generation-feedback.js");
const recipes = require("../../static/generation-recipes.js");
const deferred = () => { let resolve; return { promise: new Promise(done => { resolve = done; }), resolve: value => resolve(value) }; };

test("optional reads coalesce and preserve known data through errors and retry", async () => {
  const gate = deferred(); let count = 0, fail = false;
  const resource = feedback.createResource({ initialValue: [], load: () => { count++; if (fail) throw feedback.failure("network", "Read unavailable"); return count === 1 ? gate.promise : [2]; } });
  const first = resource.refresh(), second = resource.refresh(); assert.equal(first, second); assert.equal(count, 1);
  gate.resolve([1]); await first; assert.deepEqual(resource.snapshot().value, [1]);
  fail = true; assert.equal(await resource.refresh(), false);
  assert.equal(resource.snapshot().loading, false); assert.deepEqual(resource.snapshot().value, [1]);
  assert.equal(resource.snapshot().message, "Read unavailable");
  fail = false; await resource.refresh(); assert.equal(resource.snapshot().phase, "ready");
});

test("pause and resume fence an abort-ignoring late reply", async () => {
  const gate = deferred(); let count = 0, signal;
  const resource = feedback.createResource({ load: value => { signal = value; return ++count === 1 ? gate.promise : "new"; } });
  const old = resource.refresh(); resource.pause(); assert.equal(signal.aborted, true);
  await resource.resume(); gate.resolve("old"); await old;
  assert.equal(resource.snapshot().value, "new");
});

test("accepted evidence fences a previous read and needs no network readback", async () => {
  const gate = deferred(), resource = feedback.createResource({ load: () => gate.promise });
  const old = resource.refresh(); resource.accept("confirmed"); gate.resolve("stale"); await old;
  assert.equal(resource.snapshot().value, "confirmed");
});

test("access loss clears known data and does not restart reading on resume", async () => {
  let fail = false, reads = 0, revoked = 0;
  const resource = feedback.createResource({ initialValue: [], onAuth: () => revoked++, load: () => { reads++; if (fail) throw feedback.failure("auth"); return ["private"]; } });
  await resource.refresh(); fail = true; await resource.refresh();
  assert.deepEqual(resource.snapshot().value, []); assert.equal(resource.snapshot().authRequired, true);
  await resource.resume(); await resource.refresh(); assert.equal(reads, 2); assert.equal(revoked, 1);
});

test("bounded JSON classifies a real redirect and malformed response without retries", async () => {
  const calls = [];
  const fetcher = async (url, options) => { calls.push({ url, options }); return { type: "opaqueredirect", status: 0 }; };
  await assert.rejects(feedback.requestJSON("/read", { fetcher }), error => error.feedbackKind === "auth");
  assert.equal(calls.length, 1); assert.equal(calls[0].options.redirect, "manual");
  assert.equal(calls[0].options.credentials, "same-origin"); assert.equal(calls[0].options.cache, "no-store");
  await assert.rejects(feedback.requestJSON("/read", { fetcher: async () => ({ ok: true, json: async () => [] }) }), error => error.feedbackKind === "invalid");
});

test("timeouts, network errors and aborts have distinct feedback", async () => {
  await assert.rejects(feedback.requestJSON("/read", { timeoutSignal: () => AbortSignal.abort(), fetcher: async () => { throw new Error("socket"); } }), error => error.feedbackKind === "timeout");
  await assert.rejects(feedback.requestJSON("/read", { signal: AbortSignal.abort(), fetcher: async () => { throw new Error("socket"); } }), error => error.feedbackKind === "aborted");
  await assert.rejects(feedback.requestJSON("/read", { message: "Try reading again", fetcher: async () => { throw new Error("private technical detail"); } }), error => error.message === "Try reading again");
});

test("presentation does not replace unchanged text or take over disabled state", () => {
  let value = "same", writes = 0;
  const classes = new Map(), attributes = new Map();
  const node = { dataset: {}, disabled: true, classList: { toggle: (name, flag) => classes.set(name, flag) }, setAttribute: (name, value) => attributes.set(name, value),
    get textContent() { return value; }, set textContent(next) { writes++; value = next; } };
  feedback.present(node, "saving", "same"); feedback.present(node, "saving", "same");
  assert.equal(writes, 0); assert.equal(attributes.get("aria-busy"), "true");
  feedback.button(node, { busy: false }); assert.equal(node.disabled, true);
  feedback.present(node, "error", "retry"); assert.equal(classes.get("is-loading"), false); assert.equal(classes.get("is-error"), true);
  assert.equal(writes, 1);
});

const recipe = (id = 1) => ({ id, name: "Saved settings", values: { positive_prompt: "A portrait", template_id: "text-to-image" } });
const recipeFixture = override => {
  let state = recipes.createState(); const calls = [];
  const controller = recipes.createController({ feedback, slice: { get: () => state, dispatch: action => state = recipes.reduce(state, action) },
    transport: async (op, body, signal) => {
      calls.push({ op, body, signal });
      if (override) { const value = await override(op, body, signal); if (value) return value; }
      return op === "read" ? { recipes: [recipe()] } : op === "save" ? { recipe: recipe(2) } : { deleted: true };
    } });
  return { controller, calls };
};

test("confirmed recipe save publishes once without a dependent list read", async () => {
  const f = recipeFixture(); await f.controller.refresh();
  const result = await f.controller.save(new URLSearchParams({ recipe_name: "New", csrf: "current" }));
  assert.equal(result.ok, true); assert.deepEqual(f.calls.map(call => call.op), ["read", "save"]);
  assert.equal(f.controller.snapshot().selectedID, "2"); assert.equal(f.controller.snapshot().items.length, 2);
});

test("pending recipe writes coalesce at the operation boundary", async () => {
  const gate = deferred(), f = recipeFixture(op => op === "save" ? gate.promise : null);
  await f.controller.refresh();
  const pending = f.controller.save(new URLSearchParams());
  assert.equal((await f.controller.save(new URLSearchParams())).ok, false);
  assert.equal(await f.controller.refresh(), false);
  gate.resolve({ recipe: recipe(2) }); await pending;
  assert.equal(f.calls.filter(call => call.op === "save").length, 1);
  assert.equal(f.calls.filter(call => call.op === "read").length, 1);
});

test("unknown write response requires a list check and is never automatically replayed", async () => {
  const f = recipeFixture(op => { if (op === "save") throw feedback.failure("network"); });
  await f.controller.refresh(); await f.controller.save(new URLSearchParams());
  assert.equal(f.controller.snapshot().needsReview, true);
  await f.controller.save(new URLSearchParams()); assert.equal(f.calls.filter(call => call.op === "save").length, 1);
  await f.controller.refresh(); assert.equal(f.controller.snapshot().needsReview, false);
  assert.equal(f.controller.snapshot().phase, "info");
  assert.equal(f.calls.filter(call => call.op === "save").length, 1);
});

test("recipe read failure clears loading while keeping known settings and selection", async () => {
  let fail = false;
  const f = recipeFixture(op => { if (op === "read" && fail) throw feedback.failure("network"); });
  await f.controller.refresh(); f.controller.select("1"); fail = true; await f.controller.refresh();
  assert.equal(f.controller.snapshot().loading, false); assert.equal(f.controller.snapshot().selectedID, "1");
  assert.equal(f.controller.snapshot().items.length, 1);
});

test("recipe departure ignores late acknowledgement and only reads on resume", async () => {
  const gate = deferred(), f = recipeFixture(op => op === "save" ? gate.promise : null);
  await f.controller.refresh(); const pending = f.controller.save(new URLSearchParams()); f.controller.pause();
  await f.controller.resume(); gate.resolve({ recipe: recipe(9) }); assert.equal((await pending).ok, false);
  assert.deepEqual(f.controller.snapshot().items.map(item => item.id), [1]);
  assert.equal(f.calls.filter(call => call.op === "save").length, 1);
});

test("recipe access loss clears settings and disables further operations", async () => {
  const f = recipeFixture(op => { if (op === "remove") throw feedback.failure("auth"); });
  await f.controller.refresh(); await f.controller.remove(new URLSearchParams({ recipe_id: "1" }));
  assert.deepEqual(f.controller.snapshot().items, []); assert.equal(f.controller.snapshot().read.authRequired, true);
  const count = f.calls.length; await f.controller.resume(); await f.controller.save(new URLSearchParams()); assert.equal(f.calls.length, count);
});
