const test = require("node:test");
const assert = require("node:assert/strict");
const gpu = require("../../static/task-gpu.js");

const source = { kind: "generation", id: "task-a", terminal: false };
const snapshot = (position = 2, code = "gpu_in_use") => ({ code, position, snapshot_at: "2099-01-01T12:00:00Z" });
const flush = async () => { for (let i = 0; i < 5; i++) await Promise.resolve(); };
function harness() {
  let time = 1000, serial = 0;
  const timers = new Map(), changes = [], requests = [], denied = [];
  const monitor = gpu.createMonitor({
    now: () => time,
    setTimer: (fn, delay) => { const id = ++serial; timers.set(id, { at: time + delay, fn }); return id; },
    clearTimer: id => timers.delete(id),
    fetchSnapshot: (selected, signal) => new Promise((resolve, reject) => requests.push({ selected, signal, resolve, reject })),
    onChange: view => changes.push(view),
    onAccessError: (error, selected) => denied.push({ error, selected })
  });
  monitor.setConnected(true);
  return { monitor, requests, denied, timers, now: () => time, view: () => changes.at(-1),
    select: (selected = source, value = snapshot()) => monitor.select(selected, value, time),
    async advance(delta) {
      const until = time + delta;
      for (;;) {
        const next = [...timers.entries()].filter(([, timer]) => timer.at <= until).sort((a, b) => a[1].at - b[1].at)[0];
        if (!next) break;
        timers.delete(next[0]); time = next[1].at; next[1].fn(); await flush();
      }
      time = until; await flush();
    },
    async respond(index, value, override = {}) {
      const request = requests[index];
      request.resolve({ kind: request.selected.kind, id: request.selected.id, gpu: value, ...override }); await flush();
    },
    async fail(index, status) { const error = new Error("fixture"); error.status = status; requests[index].reject(error); await flush(); }
  };
}

test("GPU snapshots accept only known reasons and meaningful queue positions", () => {
  for (const code of Object.keys(gpu.reasons)) assert.equal(gpu.normalize(snapshot(2, code)).code, code);
  for (const value of [undefined, {}, snapshot(2, "toString"), snapshot(2, "unknown"), { ...snapshot(), snapshot_at: "yesterday" }]) assert.equal(gpu.normalize(value), undefined);
  assert.equal(gpu.normalize(null), null);
  for (const position of [-1, 0, 1.2, "3", Infinity, Number.MAX_SAFE_INTEGER + 1]) assert.equal(gpu.normalize(snapshot(position)).position, 0);
  for (const code of ["gpu_running", "gpu_reserved", "cancellation_pending", "intent_expired", "source_terminal", "owner_unavailable"]) assert.equal(gpu.normalize(snapshot(9, code)).position, 0);
  assert.equal(gpu.normalize({ ...snapshot(), lease_token: "secret" }).lease_token, undefined);
});

test("polling is independent of source revisions and does not overlap", async () => {
  const h = harness(); h.select();
  assert.equal(h.view().position, 2);
  await h.advance(gpu.pollInterval);
  assert.equal(h.requests.length, 1);
  h.monitor.refresh(); h.monitor.refresh();
  assert.equal(h.requests.length, 1);
  await h.respond(0, snapshot(1, "admission_pending"));
  assert.equal(h.view().position, 1);
  h.select(source, snapshot(99));
  assert.equal(h.view().position, 1, "later source refresh must not replace independent GPU evidence");
  await h.advance(gpu.pollInterval);
  assert.equal(h.requests.length, 2);
  h.monitor.clear();
});

test("a future server clock cannot keep a position fresh past the monotonic age bound", async () => {
  const h = harness(); h.select();
  await h.advance(gpu.maxAge + 1);
  assert.equal(h.view().fresh, false);
  assert.equal(h.view().position, 0);
  assert.match(h.view().message, /устарели/);
  assert.equal(h.view().timestamp, snapshot().snapshot_at);
  assert.equal(h.requests.length, 1, "an unresolved poll must not start another poll");
  h.monitor.clear();
});

test("slow replies are aged from request start, not from receipt", async () => {
  const h = harness(); h.select(); h.monitor.refresh();
  await h.advance(gpu.maxAge + 1);
  await h.respond(0, snapshot(1));
  assert.equal(h.view().position, 0);
  assert.equal(h.view().fresh, false);
  h.monitor.clear();
});

test("disconnect aborts and removes position; reconnect requires another read", async () => {
  const h = harness(); h.select(); h.monitor.refresh();
  h.monitor.setConnected(false);
  assert.equal(h.requests[0].signal.aborted, true);
  assert.equal(h.view().position, 0);
  await h.advance(60000);
  assert.equal(h.requests.length, 1);
  h.monitor.setConnected(true);
  assert.equal(h.view().position, 0);
  assert.equal(h.requests.length, 2);
  await h.respond(0, snapshot(77));
  assert.equal(h.view().position, 0);
  await h.respond(1, snapshot(1));
  assert.equal(h.view().position, 1);
  h.monitor.clear();
});

test("hidden pages pause timers and resume with fresh evidence", async () => {
  const h = harness(); h.select(); h.monitor.setActive(false);
  assert.equal(h.view().position, 0);
  assert.equal(h.view().paused, true);
  await h.advance(60000);
  assert.equal(h.requests.length, 0);
  h.monitor.setActive(true);
  assert.equal(h.requests.length, 1);
  assert.equal(h.view().position, 0);
  await h.respond(0, snapshot(3));
  assert.equal(h.view().position, 3);
  h.monitor.clear();
  assert.equal(h.timers.size, 0);
});

test("a late reply cannot overwrite a new selection or a closed inspector", async () => {
  const h = harness(); h.select(); h.monitor.refresh();
  h.select({ ...source, id: "task-b" }, snapshot(4));
  assert.equal(h.requests[0].signal.aborted, true);
  await h.respond(0, snapshot(88));
  assert.equal(h.view().position, 4);
  h.monitor.refresh(); h.monitor.clear();
  await h.respond(1, snapshot(66));
  assert.equal(h.view().hidden, true);
  assert.equal(h.view().timestamp, "");
  assert.equal(h.timers.size, 0);
});

test("errors invalidate position, back off, and recover without inventing source state", async () => {
  const h = harness(); h.select(); h.monitor.refresh();
  await h.fail(0, 503);
  assert.equal(h.view().position, 0);
  assert.match(h.view().message, /Не удалось/);
  await h.advance(9999);
  assert.equal(h.requests.length, 1);
  await h.advance(1);
  assert.equal(h.requests.length, 2);
  await h.fail(1, 503);
  await h.advance(19999);
  assert.equal(h.requests.length, 2);
  await h.advance(1);
  await h.respond(2, snapshot(1));
  assert.equal(h.view().position, 1);
  assert.equal(h.view().fresh, true);
  h.monitor.clear();
});

test("wrong identity or malformed evidence cannot become a position", async () => {
  for (const override of [{ id: "foreign-task" }, { kind: "lora_training" }, { gpu: snapshot(10, "unknown") }]) {
    const h = harness(); h.select(); h.monitor.refresh();
    await h.respond(0, snapshot(10), override);
    assert.equal(h.view().position, 0);
    assert.equal(h.view().fresh, false);
    h.monitor.clear();
  }
});

test("empty active intent keeps checking but terminal empty intent stops", async () => {
  const h = harness(); h.select(source, null);
  assert.equal(h.view().hidden, false);
  assert.equal(h.view().position, 0);
  await h.advance(gpu.pollInterval);
  await h.respond(0, snapshot(2));
  assert.equal(h.view().position, 2);
  h.select({ ...source, terminal: true });
  assert.equal(h.view().hidden, false, "terminal source is not proof GPU was released");
  await h.advance(gpu.pollInterval);
  await h.respond(1, null);
  assert.equal(h.view().hidden, true);
  await h.advance(60000);
  assert.equal(h.requests.length, 2);
  h.select(source, null);
  assert.equal(h.requests.length, 3, "retry of the same source resumes monitoring");
  h.monitor.clear();
});

test("packages and series have no fabricated common queue position", async () => {
  const h = harness(); h.select();
  for (const kind of ["generation_batch", "lora_caption_series"]) {
    h.select({ ...source, kind });
    assert.equal(h.view().hidden, true);
    await h.advance(60000);
    assert.equal(h.requests.length, 0);
  }
});

test("access failure clears evidence and reports only the current selection", async () => {
  for (const status of [401, 403, 404]) {
    const h = harness(); h.select(); h.monitor.refresh();
    await h.fail(0, status);
    assert.equal(h.view().hidden, true);
    assert.equal(h.view().timestamp, "");
    assert.equal(h.denied.length, 1);
    assert.equal(h.denied[0].selected.id, source.id);
    assert.equal(h.timers.size, 0);
  }
});
