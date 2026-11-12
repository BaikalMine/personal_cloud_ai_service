const test = require("node:test");
const assert = require("node:assert/strict");
const { createPostprocessController, validPostprocessMediaID } = require("../../static/generation-postprocess-state.js");
const source = { source_job_id: "postprocess-source-0001", source_media_id: "9007199254741001" };
const draft = { ...source, options: { width: 1024, height: 1024, interpolate: 1, rtx_quality: "HIGH" } };
const clone = value => value == null ? value : JSON.parse(JSON.stringify(value));
const deferred = () => { let resolve, reject; const promise = new Promise((a, b) => { resolve = a; reject = b; }); return { promise, resolve, reject }; };
function fixture(overrides = {}) {
  let saved = null, id = 0, now = 100000;
  const calls = [], timers = new Map();
  const options = { version: 1, source: { job_id: source.source_job_id, media_id: source.source_media_id, measurement: { width: 512, height: 512 } }, upscale: [{ options: draft.options }], interpolation: [], color: [] };
  const quote = value => ({ request_id: value.request_id, state: "quoted", accepted: false, confirmation: "a".repeat(64), expires_at: new Date(now + 300000).toISOString(),
    plan: { options: value.options || draft.options }, sources: [{ job_id: source.source_job_id, media_id: source.source_media_id }], cost: 1 });
  const accepted = value => ({ request_id: value.request_id, state: "accepted", accepted: true, job_id: "postprocess-child-0001" });
  const storage = { load: () => clone(saved), save: value => { saved = clone(value); }, clear: () => { saved = null; } };
  const deps = { storage, now: () => now, newID: () => `postprocess-request-${++id}`, schedule: fn => { const id = timers.size + 1; timers.set(id, fn); return id; }, unschedule: id => timers.delete(id),
    options: async () => options, quote: async value => { calls.push(["quote", clone(value), clone(saved)]); return quote(value); },
    confirm: async value => { calls.push(["confirm", clone(value), clone(saved)]); return accepted(value); },
    status: async value => { calls.push(["status", clone(value)]); return quote(value); },
    discard: async value => { calls.push(["discard", clone(value)]); return { ...value, state: "cancelled", accepted: false }; }, ...overrides };
  return { deps, calls, options, quote, accepted, storage, timers, get saved() { return saved; }, setTime: n => { now = n; }, create: () => createPostprocessController(deps) };
}
test("quote and confirm are separate, persisted before each POST, double clicks coalesce", async () => {
  const f = fixture(), c = f.create();
  await c.open(source); assert.equal(c.snapshot().canQuote, true); assert.equal(f.calls.length, 0);
  const gate = deferred(); f.deps.quote = async value => { f.calls.push(["quote", value, f.saved]); return gate.promise; };
  const first = c.calculate(draft); await c.calculate(draft);
  assert.equal(f.calls.length, 1); assert.equal(f.saved.stage, "quote");
  gate.resolve(f.quote(f.calls[0][1])); await first;
  assert.equal(c.snapshot().canConfirm, true); assert.equal(c.snapshot().pending, true);
  await c.confirm(); assert.equal(f.calls[1][2].stage, "confirm");
  assert.equal(c.snapshot().phase, "accepted"); assert.equal(f.saved, null);
  await c.confirm(); assert.equal(f.calls.length, 2);
});
test("lost quote survives reload and 404, opening a different source cannot replace it", async () => {
  const f = fixture({ quote: async () => { throw Error("lost"); }, status: async () => { throw Object.assign(Error("missing"), { status: 404 }); } });
  let c = f.create(); await c.open(source); await c.calculate(draft);
  const saved = clone(f.saved); assert.equal(c.snapshot().phase, "uncertain");
  c = f.create(); await c.open({ source_job_id: "another-source-0001", source_media_id: "7" });
  assert.deepEqual(f.saved, saved); assert.equal(c.snapshot().current.source_media_id, source.source_media_id);
  assert.equal(c.snapshot().canQuote, false); assert.match(c.snapshot().error, /ещё может/);
  await c.discard(); assert.equal(c.snapshot().phase, "cancelled"); assert.equal(f.saved, null);
  await c.loadOptions(); assert.equal(c.snapshot().canQuote, true);
});
test("lost confirm recovers original child without another confirmation", async () => {
  const f = fixture({ confirm: async () => { throw Error("lost"); } });
  let c = f.create(); await c.open(source); await c.calculate(draft); await c.confirm();
  const id = f.saved.request_id; assert.equal(f.saved.stage, "confirm");
  f.deps.status = async value => { assert.equal(value.request_id, id); return { ...f.accepted(value), removed: true }; };
  c = f.create(); await c.open(source);
  assert.equal(c.snapshot().phase, "accepted"); assert.equal(c.snapshot().receipt.removed, true); assert.equal(f.saved, null);
});
test("mismatched receipt and source or options cannot enable confirmation or clear recovery", async () => {
  for (const edit of [q => { q.request_id = "different-request-0001"; }, q => { q.sources[0].media_id = "9007199254741000"; }, q => { q.plan.options.width = 2048; }]) {
    const f = fixture(); f.deps.quote = async value => { const q = clone(f.quote(value)); edit(q); return q; };
    const c = f.create(); await c.open(source); await c.calculate(draft);
    assert.equal(c.snapshot().phase, "uncertain"); assert.equal(c.snapshot().canConfirm, false); assert.ok(f.saved);
  }
});
test("cancel/confirm race reports acceptance rather than replacing request", async () => {
  const f = fixture(); f.deps.discard = async value => f.accepted(value);
  const c = f.create(); await c.open(source); await c.calculate(draft); await c.discard();
  assert.equal(c.snapshot().phase, "accepted"); assert.equal(f.saved, null);
});
test("storage unavailable or failed readback prevents outgoing mutations", async () => {
  for (const storage of [{ load: () => null, save: () => { throw Error(); } }, { load: () => null, save: () => {} }]) {
    const f = fixture({ storage }), c = f.create(); await c.open(source); await c.calculate(draft);
    assert.equal(c.snapshot().phase, "storage_error"); assert.equal(f.calls.length, 0);
  }
  const f = fixture({ storage: { load: () => ({ version: 1, stage: "confirm", draft: { ...draft, source_media_id: 9007199254741001 } }) } });
  const c = f.create(); await c.open(source); assert.equal(c.snapshot().phase, "storage_error");
});
test("closing aborts option reads but does not abort or forget a quote mutation", async () => {
  const gate = deferred(); let signal;
  const f = fixture({ options: (_, s) => { signal = s; return gate.promise; } }), c = f.create();
  const loading = c.open(source); c.setVisible(false); assert.equal(signal.aborted, true);
  gate.resolve(f.options); await loading; assert.equal(c.snapshot().phase, "paused");
  f.deps.options = async () => f.options; await c.open(source);
  const quoteGate = deferred(); f.deps.quote = () => quoteGate.promise;
  const mutation = c.calculate(draft); c.setVisible(false); const saved = f.saved;
  quoteGate.resolve(f.quote({ ...draft, request_id: saved.request_id })); await mutation;
  assert.equal(c.snapshot().phase, "quoted"); assert.ok(f.saved); assert.equal(f.timers.size, 0);
});
test("expiry disables confirmation but retains the discard fence", async () => {
  const f = fixture(), c = f.create(); await c.open(source); await c.calculate(draft);
  f.setTime(500000); c.setVisible(true); await c.confirm();
  assert.equal(c.snapshot().phase, "expired"); assert.equal(f.calls.length, 1); assert.ok(f.saved);
  await c.discard(); assert.equal(c.snapshot().phase, "cancelled");
});
test("media identities are canonical positive int64 strings", () => {
  for (const id of ["1", "9007199254741001", "9223372036854775807"]) assert.equal(validPostprocessMediaID(id), true);
  for (const id of [0, 1, "0", "01", "-1", "1e3", "9223372036854775808", "x"]) assert.equal(validPostprocessMediaID(id), false);
});
