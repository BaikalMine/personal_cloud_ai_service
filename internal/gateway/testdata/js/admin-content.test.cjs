const test = require("node:test");
const assert = require("node:assert/strict");
const { create } = require("../../static/admin-content.js");
const flush = () => new Promise(resolve => setImmediate(resolve));
const ok = value => ({ ok: true, status: 200, text: async () => value });

function fixture(read, apply) {
  const states = [], applied = [], timers = new Map();
  let tick, events, serial = 0, reads = 0, closed = 0;
  const owner = create({
    read: signal => { reads++; return read(signal); },
    apply: value => { if (apply) apply(value); applied.push(value); },
    state: state => states.push(state),
    poll: (callback, visible, hidden) => {
      assert.equal(visible, 30000); assert.equal(hidden, 120000); tick = callback;
      return { start() {}, stop() {} };
    },
    stream: callbacks => { events = callbacks; return { connect() {}, close() { closed++; } }; },
  }, { AbortController, setTimeout: (fn, delay) => { const id = ++serial; timers.set(id, { fn, delay }); return id; }, clearTimeout: id => timers.delete(id) });
  function run(delay) {
    const entry = [...timers].find(([, timer]) => timer.delay === delay);
    assert.ok(entry, "expected timer " + delay);
    timers.delete(entry[0]); entry[1].fn();
  }
  return { owner, states, applied, timers, events: () => events, tick: () => tick(), run, reads: () => reads, closed: () => closed };
}
test("content coalesces a revision burst into one pending reconciliation", async () => {
  let release;
  const f = fixture(() => new Promise(resolve => { release = resolve; }));
  f.owner.resume(); f.events().refresh(); f.tick(); f.events().refresh();
  assert.equal(f.reads(), 1);
  release(ok("first")); await flush();
  assert.deepEqual(f.applied, ["first"]); f.run(100);
  assert.equal(f.reads(), 2); release(ok("latest")); await flush();
  assert.deepEqual(f.applied, ["first", "latest"]); assert.equal(f.timers.size, 0);
});
test("content backs off failures, keeps data and permits explicit retry", async () => {
  let fail = false;
  const f = fixture(async () => { if (fail) throw Error("offline"); return ok("known"); });
  f.owner.resume(); await flush(); fail = true;
  await f.owner.refresh(); f.events().refresh(); f.tick();
  assert.equal(f.reads(), 2); assert.deepEqual(f.applied, ["known"]);
  f.run(1800); await flush(); assert.equal(f.reads(), 3);
  assert.ok([...f.timers.values()].some(timer => timer.delay === 3600));
  fail = false; await f.owner.refresh();
  assert.equal(f.states.at(-1), "ready"); assert.equal(f.timers.size, 0);
});
for (const response of [{ status: 401 }, { status: 403 }, { status: 200, redirected: true }]) {
  test("content stops permanently after access failure " + JSON.stringify(response), async () => {
    const f = fixture(async () => response);
    f.owner.resume(); await flush();
    f.owner.refresh(); f.tick(); f.events().refresh(); f.owner.suspend(); f.owner.resume();
    assert.equal(f.reads(), 1); assert.equal(f.states.at(-1), "access"); assert.equal(f.applied.length, 0);
    assert.equal(f.timers.size, 0); assert.ok(f.closed() > 0);
  });
}
test("content rejects a late body on pagehide and serializes bfcache recovery", async () => {
  let release, signal, count = 0;
  const f = fixture(async current => {
    signal = current;
    if (++count === 1) return { ok: true, text: () => new Promise(resolve => { release = resolve; }) };
    return ok("resumed");
  });
  f.owner.resume(); await flush();
  f.owner.suspend(); assert.equal(signal.aborted, true); f.owner.resume();
  assert.equal(f.reads(), 1); release("obsolete"); await flush();
  assert.deepEqual(f.applied, ["resumed"]); assert.equal(f.reads(), 2);
  assert.equal(f.timers.size, 0);
});
test("content aborts timed out requests and suspends retry timers", async () => {
  const f = fixture(signal => new Promise((_, reject) => signal.addEventListener("abort", () => reject(Error("aborted")))));
  f.owner.resume(); f.run(25000); await flush();
  assert.equal(f.states.at(-1), "offline"); assert.ok(f.timers.size);
  f.owner.suspend(); assert.equal(f.timers.size, 0); f.tick(); f.events().refresh(); assert.equal(f.reads(), 1);
});
test("content malformed snapshots preserve the previous applied state", async () => {
  let bad = false;
  const f = fixture(async () => ok(bad ? "bad" : "good"), value => { if (value === "bad") throw Error("invalid"); });
  f.owner.resume(); await flush(); bad = true; await f.owner.refresh();
  assert.deepEqual(f.applied, ["good"]); assert.equal(f.states.at(-1), "offline");
});
