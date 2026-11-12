const test = require("node:test");
const assert = require("node:assert/strict");
const { create } = require("../../static/operations.js");

const ok = value => ({ ok: true, status: 200, json: async () => value });
function fixture(read) {
  const applied = [], states = [], timers = new Map();
  let run, now = 0, next = 1, starts = 0, stops = 0;
  const owner = create({ read, apply: value => applied.push(value), state: state => states.push(state),
    poll: (callback, interval, hiddenInterval) => {
      assert.equal(interval, 10000); assert.equal(hiddenInterval, 60000);
      run = callback;
      return { start() { starts++; }, stop() { stops++; } };
    },
  }, { AbortController, Date: { now: () => now }, setTimeout: fn => { const id = next++; timers.set(id, fn); return id; }, clearTimeout: id => timers.delete(id) });
  return { owner, applied, states, timers, tick: () => run(), advance: value => { now += value; }, stopped: () => stops, started: () => starts };
}

test("operations coalesces manual, timer and visibility reads", async () => {
  let release, reads = 0;
  const f = fixture(() => { reads++; return new Promise(resolve => { release = resolve; }); });
  const pending = f.owner.start();
  f.owner.refresh(); f.tick(); f.owner.visibility(false);
  assert.equal(reads, 1);
  release(ok({ revision: 1 })); await pending;
  assert.deepEqual(f.applied, [{ revision: 1 }]);
  assert.equal(f.states.at(-1), "ready"); assert.equal(f.timers.size, 0);
});

test("operations keeps the last state and backs off transient failures, manual retry works", async () => {
  let fail = false, reads = 0;
  const f = fixture(async () => { reads++; if (fail) throw new Error("offline"); return ok(reads); });
  await f.owner.start(); fail = true; await f.owner.refresh();
  await f.tick(); assert.equal(reads, 2); assert.deepEqual(f.applied, [1]);
  f.advance(20000); await f.tick(); assert.equal(reads, 3);
  fail = false; await f.owner.refresh(); assert.deepEqual(f.applied, [1, 4]);
  assert.equal(f.states.at(-1), "ready");
});

for (const response of [{ status: 401 }, { status: 403 }, { status: 200, redirected: true }]) {
  test(`operations permanently stops on access loss ${JSON.stringify(response)}`, async () => {
    let reads = 0;
    const f = fixture(async () => { reads++; return response; });
    await f.owner.start(); await f.owner.refresh(); await f.tick(); f.owner.suspend(); f.owner.resume(); f.owner.visibility(false);
    assert.equal(reads, 1); assert.equal(f.states.at(-1), "access"); assert.equal(f.applied.length, 0);
    assert.ok(f.stopped() > 0);
  });
}

test("operations rejects a late JSON response after pagehide and refreshes after return", async () => {
  let release, signal, reads = 0;
  const f = fixture(async current => {
    signal = current; reads++;
    if (reads === 1) return { ok: true, json: () => new Promise(resolve => { release = resolve; }) };
    return ok("new");
  });
  const old = f.owner.start(); await Promise.resolve();
  f.owner.suspend(); assert.equal(signal.aborted, true); f.owner.resume();
  assert.equal(reads, 1);
  release("late"); await old; await Promise.resolve(); await Promise.resolve();
  assert.deepEqual(f.applied, ["new"]); assert.equal(reads, 2);
});

test("operations timeout aborts a read and enables recovery", async () => {
  const f = fixture(signal => new Promise((_, reject) => signal.addEventListener("abort", () => reject(new Error("aborted")))));
  const pending = f.owner.start();
  [...f.timers.values()][0](); await pending;
  assert.equal(f.states.at(-1), "offline"); assert.equal(f.timers.size, 0);
});
