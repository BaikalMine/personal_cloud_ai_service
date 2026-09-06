const assert = require("node:assert/strict");
const test = require("node:test");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const source = fs.readFileSync(path.join(__dirname, "../../static/notifications.js"), "utf8");
const tick = () => new Promise(resolve => setImmediate(resolve));
function fixture() {
  const listeners = {}, streams = [], loads = [], timers = new Map();
  let nextTimer = 0;
  class Events {
    constructor(url) { this.url = url; this.closed = false; streams.push(this); }
    addEventListener() {}
    close() { this.closed = true; }
  }
  const center = { dataset: {}, querySelector: () => null, contains: () => false };
  const document = { querySelector: selector => selector === "[data-notification-center]" ? center : null, querySelectorAll: () => [], addEventListener() {} };
  const window = { EventSource: Events, addEventListener: (name, listener) => { listeners[name] = listener; },
    setInterval: fn => { timers.set(++nextTimer, fn); return nextTimer; }, clearInterval: id => timers.delete(id) };
  const fetch = (url, options) => new Promise(resolve => loads.push({ url, options, finish: () => resolve({ ok: true, json: async () => ({ notifications: [], summary: {} }) }) }));
  vm.runInNewContext(source, { document, window, fetch, EventSource: Events, AbortController, URLSearchParams });
  return { listeners, streams, loads, timers };
}

test("notifications cannot reopen a stream after a late load on a departed page", async () => {
  const f = fixture();
  assert.equal(f.loads.length, 1);
  f.listeners.pagehide();
  assert.equal(f.loads[0].options.signal.aborted, true);
  assert.equal(f.timers.size, 0);
  f.loads[0].finish(); await tick();
  assert.equal(f.streams.length, 0);
});

test("notification streams resume once after back-forward cache and survive cancelled unload", async () => {
  const f = fixture();
  f.loads[0].finish(); await tick();
  assert.equal(f.streams.length, 1);
  assert.equal(f.listeners.beforeunload, undefined);
  f.listeners.pagehide();
  assert.equal(f.streams[0].closed, true);
  f.listeners.pageshow({ persisted: true });
  f.listeners.pageshow({ persisted: true });
  assert.equal(f.loads.length, 2);
  assert.equal(f.timers.size, 1);
  f.loads[1].finish(); await tick();
  assert.equal(f.streams.filter(stream => !stream.closed).length, 1);
});
