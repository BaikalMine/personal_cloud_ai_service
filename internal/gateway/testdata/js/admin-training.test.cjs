const test = require("node:test");
const assert = require("node:assert/strict");
const { create } = require("../../static/admin-training.js");
const ok = value => ({ ok: true, status: 200, json: async () => value });
function fixture(read) {
  const applied = [], states = [], timers = new Map(); let next = 1, blocked = false;
  const owner = create({ read, blocked: () => blocked, apply: value => applied.push(value), state: value => states.push(value) },
    { AbortController, setTimeout(fn, delay) { const id = next++; timers.set(id, { fn, delay }); return id; }, clearTimeout(id) { timers.delete(id); } });
  return { owner, applied, states, timers, block(value) { blocked = value; } };
}
test("admin training coalesces reads and defers an in-flight fragment during confirmation", async () => {
  let resolve, reads = 0;
  const f = fixture(() => { reads++; return new Promise(r => { resolve = r; }); });
  const pending = f.owner.refresh(); f.owner.refresh(); assert.equal(reads, 1);
  f.block(true); resolve(ok("old")); await pending;
  assert.deepEqual(f.applied, []); assert.equal(f.states.at(-1), "deferred");
  await f.owner.refresh(); assert.equal(reads, 1);
  f.block(false); const fresh = f.owner.refresh(); resolve(ok("new")); await fresh;
  assert.deepEqual(f.applied, ["new"]);
});
test("admin training hidden/pagehide invalidates reads, bfcache resumes exactly once", async () => {
  let release, signal, reads = 0;
  const f = fixture(async s => { signal = s; if (++reads === 1) return { ok: true, json: () => new Promise(r => { release = r; }) }; return ok("new"); });
  const pending = f.owner.refresh(); await Promise.resolve(); f.owner.suspend(); f.owner.resume();
  assert.equal(signal.aborted, true); assert.equal(reads, 1);
  release("old"); await pending; await Promise.resolve(); await Promise.resolve();
  assert.deepEqual(f.applied, ["new"]); assert.equal(reads, 2);
  f.owner.visibility(true); await f.owner.refresh(); assert.equal(reads, 2); assert.equal(f.timers.size, 0);
  f.owner.visibility(false); await Promise.resolve(); await Promise.resolve(); assert.equal(reads, 3);
});
for (const response of [{ status: 401 }, { status: 403 }, { redirected: true, status: 200 }]) {
  test("admin training permanently stops on access loss " + JSON.stringify(response), async () => {
    let reads = 0; const f = fixture(async () => { reads++; return response; });
    await f.owner.refresh(); f.owner.suspend(); f.owner.resume(); f.owner.visibility(false); await f.owner.refresh();
    assert.equal(reads, 1); assert.equal(f.states.at(-1), "access"); assert.equal(f.timers.size, 0);
  });
}
test("admin training timeout backs off, preserves known rows and manual retry recovers", async () => {
  let fail = false;
  const f = fixture(signal => fail ? new Promise((_, reject) => signal.addEventListener("abort", () => reject(new Error("abort")))) : Promise.resolve(ok("data")));
  await f.owner.refresh(); fail = true; const pending = f.owner.refresh();
  [...f.timers.values()].find(t => t.delay === 25000).fn(); await pending;
  assert.equal(f.states.at(-1), "offline"); assert.deepEqual(f.applied, ["data"]);
  assert.equal([...f.timers.values()][0].delay, 60000);
  fail = false; await f.owner.refresh(); assert.equal(f.states.at(-1), "ready");
});
