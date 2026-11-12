const test = require("node:test");
const assert = require("node:assert/strict");
const live = require("../../static/live-events.js");

function environment() {
  let id = 0;
  const timers = new Map();
  const streams = [];
  class EventSource {
    constructor(url) { this.url = url; this.events = {}; streams.push(this); }
    addEventListener(name, callback) { this.events[name] = callback; }
    emit(name, data) { this.events[name]?.({ data }); }
    close() { this.closed = true; }
  }
  return {
    EventSource, Math: { random: () => 0.5 }, document: { hidden: false }, streams, timers,
    setTimeout(fn, delay) { const key = ++id; timers.set(key, { fn, delay }); return key; },
    clearTimeout(key) { timers.delete(key); },
    async next() { const [key, timer] = timers.entries().next().value; timers.delete(key); await timer.fn(); return timer.delay; },
  };
}

test("live reconnect coalesces, backs off, reads current cursor and ignores old stream", async () => {
  const env = environment();
  let revision = 8;
  let received = 0;
  const stream = live.create({ url: () => `/?since=${revision}`, events: { ready: () => received++, jobs: () => received++ } }, env);
  stream.connect(); stream.connect();
  assert.equal(env.streams.length, 1);
  const first = env.streams[0];
  first.emit("error"); first.emit("error");
  assert.equal(env.timers.size, 1);
  assert.equal(first.closed, true);
  revision = 12;
  assert.equal(await env.next(), 2000);
  assert.equal(env.streams[1].url, "/?since=12");
  first.emit("jobs"); assert.equal(received, 0);
  env.streams[1].emit("error"); assert.equal(await env.next(), 4000);
  env.streams[2].emit("ready"); assert.equal(received, 1);
  env.streams[2].emit("error"); assert.equal(await env.next(), 2000);
  for (let i = 0; i < 9; i++) { env.streams.at(-1).emit("error"); assert.ok((await env.next()) <= 30000); }
  stream.close(); assert.equal(env.timers.size, 0);
  env.streams.at(-1).emit("jobs"); assert.equal(received, 1);
  stream.connect(); assert.equal(env.streams.at(-1).closed, undefined);
});

test("constructor failure and denied access cannot leave a retry after close", () => {
  const env = environment();
  env.EventSource = class { constructor() { throw new Error("offline"); } };
  let stream;
  stream = live.create({ url: () => "/", onError: () => stream.close() }, env);
  stream.connect(); assert.equal(env.timers.size, 0);
});

test("no EventSource has bounded polling fallback, without connection spin", () => {
  const env = environment(); delete env.EventSource;
  let state;
  const stream = live.create({ url: () => "/", onState: next => { state = next; } }, env);
  stream.connect(); assert.equal(state, "offline"); assert.equal(env.timers.size, 0);
});

test("optional poll slows in hidden pages, serializes reads and stays stopped", async () => {
  const env = environment();
  let resolve;
  let calls = 0;
  const poll = live.poll(() => { calls++; return new Promise(done => { resolve = done; }); }, 5000, 30000, env);
  poll.start(); poll.start(); assert.equal(env.timers.size, 1);
  const run = env.next(); assert.equal(calls, 1); assert.equal(env.timers.size, 0);
  poll.start(); assert.equal(env.timers.size, 0);
  env.document.hidden = true; resolve(); await run;
  assert.equal([...env.timers.values()][0].delay, 30000);
  const pending = env.next(); poll.stop(); resolve(); await pending;
  assert.equal(env.timers.size, 0);
  poll.start(); assert.equal([...env.timers.values()][0].delay, 30000);
  poll.stop(); env.document.hidden = false; poll.start();
  assert.equal([...env.timers.values()][0].delay, 5000);
});
