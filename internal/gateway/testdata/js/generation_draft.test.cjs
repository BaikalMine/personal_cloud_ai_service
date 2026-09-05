const assert = require("node:assert/strict");
const test = require("node:test");
const { createController, bindUI } = require("../../static/generation-draft.js");
const media = require("../../static/generation-media.js");

const deferred = () => {
  let resolve;
  const promise = new Promise((done) => { resolve = done; });
  return { promise, resolve };
};
function fixture(initial = null) {
  let remote = initial;
  let sequence = initial?.revision || 0;
  const requests = [];
  const server = async (op, body) => {
    requests.push({ op, body: body && new URLSearchParams(body) });
    if (op === "load") return { ok: true, payload: { draft: remote } };
    if (Number(body.get("draft_revision")) !== (remote?.revision || 0)) return { ok: false, status: 409, payload: { draft: remote } };
    if (op === "delete") remote = null;
    else remote = { revision: ++sequence, values: Object.fromEntries(body), assets: [] };
    return { ok: true, payload: { draft: remote } };
  };
  const client = (overrides = {}) => {
    const applied = [];
    let text = "local prompt";
    const states = [];
    const timers = new Map();
    let timerID = 0;
    const controller = createController({
      transport: server, capture: () => new URLSearchParams({ positive_prompt: text }),
      apply: (value) => { text = value.values.positive_prompt || ""; applied.push(value); },
      onState: (state) => states.push(state),
      schedule: (callback) => { timers.set(++timerID, callback); return timerID; },
      unschedule: (id) => timers.delete(id), ...overrides,
    });
    return { controller, applied, states, timers, edit(value) { text = value; controller.markDirty(); }, text: () => text };
  };
  return { client, server, requests, remote: () => remote };
}

test("draft loads without writing defaults and restores the saved values", async () => {
  const f = fixture({ revision: 7, values: { positive_prompt: "saved" }, assets: [] });
  const c = f.client();
  await c.controller.load();
  assert.equal(c.text(), "saved");
  assert.equal(c.controller.snapshot().revision, 7);
  assert.equal(c.controller.snapshot().dirty, false);
  assert.equal(f.requests.length, 1);
  assert.equal(await c.controller.flush(), false);
});

test("draft concurrent clients require an explicit choice before overwriting", async () => {
  const f = fixture();
  const a = f.client(), b = f.client();
  await a.controller.load(); await b.controller.load();
  a.edit("first device"); await a.controller.flush();
  b.edit("second device"); await b.controller.flush();
  assert.equal(b.controller.snapshot().status, "conflict");
  assert.equal(f.remote().values.positive_prompt, "first device");
  b.edit("second device amended");
  assert.equal(await b.controller.flush(), false);
  await b.controller.keepLocal();
  assert.equal(f.remote().values.positive_prompt, "second device amended");
  assert.equal(b.controller.snapshot().dirty, false);
});

test("draft can take remote version without an extra save", async () => {
  const f = fixture({ revision: 3, values: { positive_prompt: "remote" }, assets: [] });
  const c = f.client();
  await c.controller.load({ preserveLocal: true });
  assert.equal(c.text(), "local prompt");
  assert.equal(c.controller.snapshot().status, "conflict");
  await c.controller.useRemote();
  assert.equal(c.text(), "remote");
  assert.equal(c.controller.snapshot().dirty, false);
  assert.equal(f.requests.length, 1);
});

test("a repeat or library deep link becomes a draft without requiring another edit", async () => {
  const f = fixture(); const c = f.client();
  await c.controller.load({ preserveLocal: true });
  assert.equal(c.controller.snapshot().dirty, true);
  await c.controller.flush();
  assert.equal(f.remote().values.positive_prompt, "local prompt");
});

test("draft edits during startup are not overwritten by late load", async () => {
  const gate = deferred();
  const f = fixture();
  const c = f.client({ transport: () => gate.promise });
  const loading = c.controller.load();
  c.edit("typed while offline");
  gate.resolve({ ok: true, payload: { draft: { revision: 4, values: { positive_prompt: "remote" } } } });
  await loading;
  assert.equal(c.text(), "typed while offline");
  assert.equal(c.applied.length, 0);
  assert.equal(c.controller.snapshot().status, "conflict");
});

test("draft preserves edits made during a save and serializes requests", async () => {
  const f = fixture();
  const gate = deferred();
  let wait = true;
  const c = f.client({ transport: async (op, body) => {
    if (op === "save" && wait) { wait = false; await gate.promise; }
    return f.server(op, body);
  }});
  await c.controller.load();
  c.edit("first");
  const saving = c.controller.flush();
  await Promise.resolve();
  c.edit("newer");
  assert.equal(c.controller.flush(), saving);
  gate.resolve(); await saving;
  assert.equal(c.controller.snapshot().dirty, true);
  await c.controller.flush();
  assert.equal(f.remote().values.positive_prompt, "newer");
  assert.equal(c.controller.snapshot().dirty, false);
});

test("draft save error leaves work dirty and allows a retry", async () => {
  const f = fixture();
  let fail = true;
  const c = f.client({ transport: (op, body) => {
    if (op === "save" && fail) return Promise.reject(new Error("offline"));
    return f.server(op, body);
  }});
  await c.controller.load(); c.edit("keep me");
  assert.equal(await c.controller.flush(), false);
  assert.equal(c.controller.snapshot().status, "error");
  assert.equal(c.controller.snapshot().dirty, true);
  fail = false; await c.controller.flush();
  assert.equal(f.remote().values.positive_prompt, "keep me");
});

test("unsupported draft is retained as a conflict, not replaced with defaults", async () => {
  const f = fixture({ revision: 2, values: { model: "unavailable" } });
  const c = f.client({ apply: () => { throw new Error("model unavailable"); } });
  await c.controller.load();
  assert.equal(c.controller.snapshot().status, "conflict");
  c.edit("new work"); await c.controller.flush();
  assert.equal(f.remote().values.model, "unavailable");
});

test("deleting a draft does not immediately recreate it, next edit can save again", async () => {
  const f = fixture(); const c = f.client();
  await c.controller.load(); c.edit("old"); await c.controller.flush();
  const revision = c.controller.snapshot().revision;
  assert.equal(await c.controller.remove(), true);
  assert.equal(f.remote(), null);
  assert.equal(await c.controller.flush(), false);
  c.edit("new"); await c.controller.flush();
  assert.ok(c.controller.snapshot().revision > revision);
  assert.equal(f.remote().values.positive_prompt, "new");
});

test("stale delete and delete-recreate both conflict", async () => {
  const f = fixture({ revision: 1, values: { positive_prompt: "old" } });
  const a = f.client(), b = f.client();
  await a.controller.load(); await b.controller.load();
  a.edit("new"); await a.controller.flush();
  assert.equal(await b.controller.remove(), false);
  assert.equal(b.controller.snapshot().status, "conflict");
  await b.controller.useRemote();
  await a.controller.remove(); a.edit("recreated"); await a.controller.flush();
  b.edit("stale"); await b.controller.flush();
  assert.equal(b.controller.snapshot().status, "conflict");
  assert.equal(f.remote().values.positive_prompt, "recreated");
});

test("restored references keep their source kind instead of becoming device files", () => {
  const source = { kind: "restored", name: "portrait.png", url: "/generate/draft/asset?id=owned" };
  const state = media.reduce(media.createState(), { type: "SELECT_SOURCE", slot: 1, source });
  assert.equal(state.sources[1].kind, "restored");
  assert.equal(media.referenceMetadata({ source, uploaded: "owned/portrait.png" }).source, "restored");
});

test("draft toolbar warns about unsaved files even after text is saved", async () => {
  const elements = new Map();
  const element = () => ({ dataset: {}, hidden: false, disabled: false, textContent: "", listeners: {}, addEventListener(name, callback) { this.listeners[name] = callback; } });
  const document = { ...element(), getElementById(id) { if (!elements.has(id)) elements.set(id, element()); return elements.get(id); } };
  const window = { ...element(), confirm: () => true };
  const controller = bindUI({ document, window, capture: () => new URLSearchParams(), apply: () => {},
    transport: async () => ({ ok: true, payload: { draft: null } }), hasUnsavedFiles: () => true });
  await controller.load();
  let prevented = false;
  const event = { preventDefault() { prevented = true; } };
  window.listeners.beforeunload(event);
  assert.equal(prevented, true);
  assert.equal(event.returnValue, "");
  assert.equal(elements.get("generation-draft-delete").disabled, true);
});
