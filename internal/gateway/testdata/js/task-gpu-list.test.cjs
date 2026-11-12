const test = require("node:test");
const assert = require("node:assert/strict");
const gpu = require("../../static/task-gpu.js");

const a = { kind: "generation", id: "a", terminal: false };
const b = { kind: "lora_training", id: "b", terminal: false };
const c = { kind: "lora_caption", id: "c", terminal: true };
const stamp = "2099-01-01T12:00:00Z";
const sample = (position = 2) => ({ code: "waiting_turn", position, snapshot_at: stamp });
const batch = (sources, value = sample()) => ({ snapshot_at: stamp, tasks: sources.map(source => ({ kind: source.kind, id: source.id, available: true, gpu: value })) });
const flush = async () => { for (let i = 0; i < 5; i++) await Promise.resolve(); };
function harness() {
  let time = 1000, serial = 0;
  const timers = new Map(), changes = [], requests = [], denied = [], unavailable = [];
  const monitor = gpu.createListMonitor({
    now: () => time,
    setTimer: (fn, delay) => { const id = ++serial; timers.set(id, { at: time + delay, fn }); return id; },
    clearTimer: id => timers.delete(id),
    fetchSnapshots: (sources, signal) => new Promise((resolve, reject) => requests.push({ sources, signal, resolve, reject })),
    onChange: views => changes.push(views), onUnavailable: sources => unavailable.push(sources), onAccessError: error => denied.push(error)
  });
  monitor.setConnected(true);
  return { monitor, requests, denied, unavailable, timers, view: (id = "a") => changes.at(-1).find(view => view.key.endsWith(`:${id}`)),
    async advance(delta) {
      const until = time + delta;
      for (;;) {
        const next = [...timers.entries()].filter(([, timer]) => timer.at <= until).sort((a, b) => a[1].at - b[1].at)[0];
        if (!next) break;
        timers.delete(next[0]); time = next[1].at; next[1].fn(); await flush();
      }
      time = until; await flush();
    },
    async respond(index, value = sample(), override) { requests[index].resolve(override || batch(requests[index].sources, value)); await flush(); },
    async fail(index, status) { const error = new Error("fixture"); error.status = status; requests[index].reject(error); await flush(); }
  };
}

test("batch validation rejects partial, extra, duplicate and inconsistent samples atomically", () => {
  const valid = batch([a, b]);
  assert.equal(gpu.normalizeBatch(valid, [a, b]).entries.size, 2);
  assert.equal(gpu.normalizeBatch({ ...valid, tasks: [...valid.tasks].reverse() }, [a, b]).entries.size, 2);
  const variants = [null, {}, { ...valid, snapshot_at: "yesterday" }, { ...valid, tasks: valid.tasks.slice(1) },
    { ...valid, tasks: [...valid.tasks, valid.tasks[0]] }, { ...valid, tasks: [valid.tasks[0], valid.tasks[0]] }];
  for (const field of [{ id: "foreign" }, { id: ["b"] }, { kind: ["lora_training"] }, { kind: "generation_batch" }, { available: "true" }, { available: false }, { gpu: {} },
    { gpu: { ...sample(), snapshot_at: "2099-01-01T12:00:01Z" } }]) {
    variants.push({ ...valid, tasks: [valid.tasks[0], { ...valid.tasks[1], ...field }] });
  }
  for (const value of variants) assert.equal(gpu.normalizeBatch(value, [a, b]), null);
  const missing = { ...valid, tasks: valid.tasks.map(entry => ({ ...entry, available: false, gpu: null })) };
  assert.equal(gpu.normalizeBatch(missing, [a, b]).entries.get("generation:a").available, false);
});

test("one bounded poll covers all visible sources without overlap or task-revision inputs", async () => {
  const h = harness(); h.monitor.select([a, b, c]);
  assert.equal(h.requests.length, 1); assert.equal(h.requests[0].sources.length, 3);
  h.monitor.refresh(); h.monitor.select([c, b, a]);
  assert.equal(h.requests.length, 1);
  await h.respond(0); assert.equal(h.view().position, 2);
  assert.equal(h.timers.size, 2, "one polling and one expiry timer for the entire set");
  await h.advance(5000); assert.equal(h.requests.length, 2);
  await h.respond(1, sample(1)); assert.equal(h.view().position, 1);
  h.monitor.clear(); assert.equal(h.timers.size, 0);
});

test("invalid identities and aggregates are excluded and the monitor caps at 32", () => {
  const h = harness();
  h.monitor.select([null, { ...a, id: "../a" }, { ...a, kind: "generation_batch" }, ...Array.from({ length: 80 }, (_, i) => ({ ...a, id: `task-${i}` }))]);
  assert.equal(h.requests[0].sources.length, 32);
  h.monitor.clear();
});

test("viewport changes abort obsolete reads, drop departed samples and ignore late replies", async () => {
  const h = harness(); h.monitor.select([a, b]); await h.respond(0);
  h.monitor.refresh(); h.monitor.select([b, c]);
  assert.equal(h.requests[1].signal.aborted, true); assert.equal(h.view(), undefined);
  assert.equal(h.view("b").position, 2, "retained member keeps its still-fresh evidence");
  await h.respond(1, sample(99)); assert.equal(h.view("b").position, 2);
  await h.respond(2, sample(1)); assert.equal(h.view("b").position, 1);
  h.monitor.select([a]); assert.equal(h.view().position, 0, "reentered row needs a new read");
  await h.respond(3); h.monitor.clear();
});

test("future timestamps and slow responses never extend monotonic freshness", async () => {
  const h = harness(); h.monitor.select([a, b]); await h.respond(0);
  await h.advance(5000); assert.equal(h.requests.length, 2);
  await h.advance(10001); assert.equal(h.view().position, 0); assert.match(h.view().message, /устарели/);
  assert.equal(h.view("b").position, 0); assert.equal(h.view().timestamp, stamp);
  await h.respond(1, sample(99)); assert.equal(h.view().position, 0);
  await h.advance(5000); await h.respond(2, sample(1)); assert.equal(h.view().position, 1);
  h.monitor.clear();
});

test("failure clears ranks immediately, retries with backoff and accepts manual recovery", async () => {
  const h = harness(); h.monitor.select([a, b]); await h.respond(0);
  h.monitor.refresh(); await h.fail(1, 503);
  assert.equal(h.view().position, 0); assert.match(h.view().message, /не удалось/);
  assert.equal(h.denied.length, 0);
  await h.advance(9999); assert.equal(h.requests.length, 2);
  await h.advance(1); assert.equal(h.requests.length, 3); await h.fail(2, 503);
  await h.advance(19999); assert.equal(h.requests.length, 3);
  h.monitor.refresh(); await h.respond(3); assert.equal(h.view().position, 2);
  h.monitor.clear();
});

test("malformed response invalidates every requested member, never a partial update", async () => {
  const h = harness(); h.monitor.select([a, b]); await h.respond(0);
  h.monitor.refresh(); const response = batch([a, b], sample(99)); response.tasks[1].gpu.code = "not-a-reason";
  await h.respond(1, null, response);
  assert.equal(h.view().position, 0); assert.equal(h.view("b").position, 0);
  assert.match(h.view().message, /не удалось/); h.monitor.clear();
});

for (const setter of ["setActive", "setConnected"]) test(`${setter} pauses timers, aborts pending and requires fresh evidence on return`, async () => {
  const h = harness(); h.monitor.select([a]); await h.respond(0); h.monitor.refresh();
  h.monitor[setter](false); assert.equal(h.requests[1].signal.aborted, true); assert.equal(h.view().position, 0);
  assert.equal(h.timers.size, 0); await h.advance(60000); assert.equal(h.requests.length, 2);
  h.monitor[setter](true); assert.equal(h.view().position, 0); assert.equal(h.requests.length, 3);
  await h.respond(1, sample(99)); assert.equal(h.view().position, 0);
  await h.respond(2, sample(1)); assert.equal(h.view().position, 1); h.monitor.clear();
});

test("terminal sources remain watched until null; active null sources keep polling", async () => {
  const h = harness(); h.monitor.select([a, c]); await h.respond(0);
  assert.equal(h.view("c").hidden, false);
  await h.advance(5000); await h.respond(1, null);
  assert.equal(h.view("c").hidden, true); assert.equal(h.view().hidden, true);
  await h.advance(5000); assert.deepEqual(h.requests[2].sources, [a]); await h.respond(2, null);
  h.monitor.select([{ ...a, terminal: true }, c]);
  assert.equal(h.requests[3].sources.length, 1, "source lifecycle change requires fresh release evidence");
  await h.respond(3, null); await h.advance(60000); assert.equal(h.requests.length, 4);
  h.monitor.clear();
});

test("unavailable entries trigger one source revalidation and never claim a queue rank", async () => {
  const h = harness(); h.monitor.select([a, b]);
  const response = batch([a, b]); response.tasks[0] = { ...response.tasks[0], available: false, gpu: null };
  await h.respond(0, null, response);
  assert.deepEqual(h.unavailable, [[a]]); assert.equal(h.view().hidden, true); assert.equal(h.view().position, 0);
  h.monitor.select([a, b]); await h.advance(5000); assert.deepEqual(h.requests[1].sources, [b]);
  await h.respond(1); assert.equal(h.unavailable.length, 1);
  h.monitor.refresh({ force: true }); assert.deepEqual(h.requests[2].sources, [a, b]);
  await h.respond(2); assert.equal(h.view().position, 2); h.monitor.clear();
});

for (const status of [401, 403]) test(`access failure ${status} drops every private sample and stops`, async () => {
  const h = harness(); h.monitor.select([a, b]); await h.respond(0); h.monitor.refresh(); await h.fail(1, status);
  assert.equal(h.view(), undefined); assert.equal(h.denied.length, 1); assert.equal(h.timers.size, 0);
});
