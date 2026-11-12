const test = require("node:test");
const assert = require("node:assert/strict");
const { create } = require("../../static/admin-resources.js");
const ok = value => ({ ok: true, status: 200, json: async () => value });
function fixture(read) {
  const applied = [], states = [], timers = new Map(); let id = 0, now = 0, tick, blocked = false, starts = 0, stops = 0;
  const owner = create({ read, blocked: () => blocked, apply: value => applied.push(value), state: value => states.push(value), poll(run, interval, hidden) { assert.equal(interval, 30000); assert.equal(hidden, 120000); tick = run; return { start() { starts++; }, stop() { stops++; } }; } },
    { Date: { now: () => now }, AbortController, setTimeout(fn, delay) { timers.set(++id, { fn, delay }); return id; }, clearTimeout(key) { timers.delete(key); } });
  return { owner, applied, states, timers, tick: () => tick(), block(value) { blocked = value; }, time(value) { now = value; }, counts: () => ({ starts, stops }) };
}
test("resources coalesce, defer confirmation and reread on close without replacing forms", async () => {
  let resolve, reads = 0;
  const f = fixture(() => { reads++; return new Promise(r => { resolve = r; }); });
  const pending = f.owner.resume(); f.owner.refresh(); assert.equal(reads, 1);
  f.block(true); resolve(ok("old")); await pending; assert.deepEqual(f.applied, []); assert.equal(f.states.at(-1), "deferred");
  f.block(false); const next = f.owner.unblock(); resolve(ok("fresh")); await next;
  assert.deepEqual(f.applied, ["fresh"]); assert.equal(reads, 2);
});
test("resources hidden/pagehide fences late JSON and resumes exactly one read", async () => {
  let release, signal, reads = 0;
  const f = fixture(async s => { signal = s; return ++reads === 1 ? { ok: true, json: () => new Promise(r => { release = r; }) } : ok("new"); });
  const pending = f.owner.resume(); await Promise.resolve(); f.owner.suspend(); f.owner.resume(); f.owner.resume();
  assert.equal(signal.aborted, true); release("old"); await pending; await Promise.resolve(); await Promise.resolve();
  assert.deepEqual(f.applied, ["new"]); assert.equal(reads, 2);
  f.owner.suspend(); await f.tick(); await f.owner.refresh(); assert.equal(reads, 2); assert.equal(f.timers.size, 0);
});
for (const response of [{ status: 401 }, { status: 403 }, { redirected: true, status: 200 }]) {
  test("resources stop permanently on access loss " + JSON.stringify(response), async () => {
    let reads = 0; const f = fixture(async () => { reads++; return response; });
    await f.owner.resume(); f.owner.suspend(); f.owner.resume(); await f.owner.refresh(); await f.tick();
    assert.equal(reads, 1); assert.equal(f.states.at(-1), "access"); assert.equal(f.timers.size, 0);
  });
}
test("resources timeout keeps data and backs off, explicit retry recovers", async () => {
  let fail = false, reads = 0;
  const f = fixture(signal => { reads++; return fail ? new Promise((_, reject) => signal.addEventListener("abort", () => reject(new Error("abort")))) : Promise.resolve(ok("data")); });
  await f.owner.resume(); fail = true; const pending = f.owner.refresh(); [...f.timers.values()][0].fn(); await pending;
  assert.equal(f.states.at(-1), "offline"); assert.deepEqual(f.applied, ["data"]);
  await f.tick(); assert.equal(reads, 2); fail = false; await f.owner.refresh(); assert.equal(f.states.at(-1), "ready");
});
