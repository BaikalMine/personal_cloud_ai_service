const assert = require("node:assert/strict");
const test = require("node:test");
const { createController, bindUI, createSession, createTransport } = require("../../static/generation-draft.js");
const feedback = require("../../static/generation-feedback.js");
const media = require("../../static/generation-media.js");

const deferred = () => {
  let resolve;
  const promise = new Promise((done) => { resolve = done; });
  return { promise, resolve };
};

test("collection opening applies a fresh draft without another autosave", async () => {
  const f = fixture({ revision: 4, values: { positive_prompt: "old" }, assets: [] }), c = f.client();
  await c.controller.load();
  assert.equal(await c.controller.replace(async revision => { assert.equal(revision, 4); return { revision: 5, values: { positive_prompt: "collection" }, assets: [] }; }), true);
  assert.equal(c.text(), "collection"); assert.equal(c.applied.at(-1).fresh, true);
  assert.equal(c.controller.snapshot().revision, 5); assert.equal(c.controller.snapshot().dirty, false);
  assert.equal(f.requests.length, 1);
});

test("edits during collection opening remain local until explicit conflict choice", async () => {
  const f = fixture({ revision: 4, values: { positive_prompt: "old" }, assets: [] }), c = f.client(), pending = deferred();
  await c.controller.load();
  const opening = c.controller.replace(() => pending.promise);
  await Promise.resolve(); await Promise.resolve();
  assert.equal(c.controller.snapshot().replacing, true);
  c.edit("edited during open");
  pending.resolve({ revision: 5, values: { positive_prompt: "collection" }, assets: [] });
  assert.equal(await opening, false);
  assert.equal(c.text(), "edited during open"); assert.equal(c.controller.snapshot().status, "conflict");
  assert.equal(await c.controller.flush(), false); assert.equal(f.requests.length, 1);
  await c.controller.useRemote(); assert.equal(c.text(), "collection");
});

test("unknown collection opening preserves old draft revision and local text", async () => {
  const f = fixture({ revision: 4, values: { positive_prompt: "old" }, assets: [] }), c = f.client();
  await c.controller.load();
  await assert.rejects(c.controller.replace(async () => { throw new Error("unknown"); }));
  assert.equal(c.text(), "old"); assert.equal(c.controller.snapshot().revision, 4);
  assert.equal(c.controller.snapshot().replacing, false); assert.equal(c.controller.snapshot().status, "error");
});
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

test("navigation settles the latest edit after an in-flight autosave", async () => {
  const f = fixture(), gate = deferred();
  let held = false;
  const c = f.client({ transport: async (op, body) => {
    if (op === "save" && !held) { held = true; await gate.promise; }
    return f.server(op, body);
  }});
  assert.equal(await c.controller.settle(), false);
  await c.controller.load();
  c.edit("first"); c.controller.flush(); await Promise.resolve();
  c.edit("latest");
  const leaving = c.controller.settle();
  gate.resolve();
  assert.equal(await leaving, true);
  assert.equal(f.remote().values.positive_prompt, "latest");
  assert.equal(c.controller.snapshot().dirty, false);
  assert.equal(f.requests.filter(request => request.op === "save").length, 2);
});

test("navigation cannot settle a failed or conflicted draft", async () => {
  const f = fixture();
  const a = f.client(), b = f.client();
  await a.controller.load(); await b.controller.load();
  a.edit("remote work"); await a.controller.flush();
  b.edit("local work");
  assert.equal(await b.controller.settle(), false);
  assert.equal(b.controller.snapshot().status, "conflict");
  assert.equal(await b.controller.settle(), false);
  assert.equal(f.remote().values.positive_prompt, "remote work");
  const c = f.client({ transport: (op, body) => op === "save" ? Promise.reject(new Error("offline")) : f.server(op, body) });
  await c.controller.load(); c.edit("keep on page");
  assert.equal(await c.controller.settle(), false);
  assert.equal(c.controller.snapshot().status, "error");
  assert.equal(c.text(), "keep on page");
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

for (const reason of ["error", "conflict", "files"]) {
  for (const accept of [true, false]) {
    test(`navigation ${accept ? "leaves" : "stays"} explicitly with ${reason}, without scrolling`, async () => {
      const elements = new Map(), assigned = [], confirmations = [];
      const element = () => ({ dataset: {}, classList: { toggle() {} }, setAttribute() {}, listeners: {},
        addEventListener(name, fn) { this.listeners[name] = fn; },
        scrollIntoView() { assert.fail("navigation must not scroll to the draft"); },
      });
      const document = { ...element(), getElementById(id) { if (!elements.has(id)) elements.set(id, element()); return elements.get(id); } };
      const window = { ...element(), location: { href: "https://example.test/generate", origin: "https://example.test", pathname: "/generate", search: "", assign: url => assigned.push(url) },
        confirm: message => { confirmations.push(message); return accept; } };
      const controller = bindUI({ document, window, feedback, capture: () => new URLSearchParams(), apply() {},
        schedule: () => 1, unschedule() {}, hasUnsavedFiles: () => reason === "files",
        transport: async op => op === "load" ? { ok: true, payload: { draft: null } } : reason === "conflict"
          ? { ok: false, status: 409, payload: { draft: { revision: 2 } } }
          : { ok: false, status: 500, payload: { error: "offline" } },
      });
      await controller.load();
      if (reason !== "files") controller.markDirty();
      let prevented = false;
      const link = { href: "https://example.test/tasks", target: "", hasAttribute: () => false };
      await document.listeners.click({ button: 0, target: { closest: () => link }, preventDefault() { prevented = true; } });
      assert.equal(prevented, true);
      assert.equal(confirmations.length, 1);
      assert.deepEqual(assigned, accept ? [link.href] : []);
      let unloadBlocked = false;
      window.listeners.beforeunload({ preventDefault() { unloadBlocked = true; } });
      assert.equal(unloadBlocked, !accept, "accepted departure must not show a second confirmation");
      window.listeners.pageshow();
      unloadBlocked = false;
      window.listeners.beforeunload({ preventDefault() { unloadBlocked = true; } });
      assert.equal(unloadBlocked, true, "back navigation restores loss protection");
    });
  }
}

test("draft toolbar warns about unsaved files even after text is saved", async () => {
  const elements = new Map();
  const element = () => ({ dataset: {}, classList: { toggle() {} }, setAttribute() {}, hidden: false, disabled: false, textContent: "", listeners: {}, addEventListener(name, callback) { this.listeners[name] = callback; } });
  const document = { ...element(), getElementById(id) { if (!elements.has(id)) elements.set(id, element()); return elements.get(id); } };
  const window = { ...element(), confirm: () => true };
  const controller = bindUI({ document, window, feedback, capture: () => new URLSearchParams(), apply: () => {},
    transport: async () => ({ ok: true, payload: { draft: null } }), hasUnsavedFiles: () => true });
  await controller.load();
  let prevented = false;
  const event = { preventDefault() { prevented = true; } };
  window.listeners.beforeunload(event);
  assert.equal(prevented, true);
  assert.equal(event.returnValue, "");
  assert.equal(elements.get("generation-draft-delete").disabled, true);
});

const sessionFixture = (initial = null, overrides = {}) => {
  const element = () => ({
    dataset: {}, classList: { toggle() {} }, setAttribute() {}, hidden: false, disabled: false, open: false, textContent: "", listeners: new Map(),
    addEventListener(name, callback) { const list = this.listeners.get(name) || []; list.push(callback); this.listeners.set(name, list); },
    dispatch(name, event = {}) { for (const listener of this.listeners.get(name) || []) listener(event); },
  });
  const elements = new Map();
  const document = { ...element(), getElementById(id) { if (!elements.has(id)) elements.set(id, element()); return elements.get(id); } };
  const window = { ...element(), confirm: () => true };
  const form = { ...element(), elements: { csrf: { value: "csrf" } } };
  const root = element(), advanced = element();
  const store = require("../../static/generation-store.js").createStore();
  const f = fixture(initial);
  let text = "local prompt", pending = [], active = 0;
  const applied = [];
  const controller = createSession({
    document, window, form, root, advanced, store, feedback, transport: f.server,
    schedule: () => 1, unschedule() {},
    parameters: {
      resolveSelection(values) { if (values.model === "unavailable") throw new Error("unavailable"); },
      draft(state) { return new URLSearchParams({ positive_prompt: state.text, pending: String(state.media.pending.length) }); },
    },
    files: { pending: () => pending, inFlight: () => active, current: item => pending.includes(item) },
    capture: () => ({ text, media: {} }),
    apply(draft) {
      applied.push(draft);
      text = draft.values.positive_prompt || "";
      form.dispatch("input");
      advanced.open = true;
      advanced.dispatch("toggle");
      store.emit("wizard:change", { previous: { step: 1 }, current: { step: 2 } });
    },
    ...overrides,
  });
  return {
    ...f, controller, form, root, advanced, store, window, document, applied,
    text: () => text, pending: entries => { pending = entries; }, active: count => { active = count; },
  };
};

test("departed draft page does not start a save on late visibility events", async () => {
  const f = sessionFixture();
  await f.controller.load();
  f.controller.markDirty();
  f.window.dispatch("pagehide");
  f.document.visibilityState = "hidden";
  f.document.dispatch("visibilitychange");
  await new Promise(setImmediate);
  assert.equal(f.requests.filter(request => request.op === "save").length, 0);
  assert.equal(f.controller.snapshot().dirty, true);
  f.window.dispatch("pageshow", { persisted: true });
  await new Promise(setImmediate);
  assert.equal(f.requests.filter(request => request.op === "save").length, 0);
  f.document.visibilityState = "visible";
  f.document.dispatch("visibilitychange");
  f.document.visibilityState = "hidden";
  f.document.dispatch("visibilitychange");
  await new Promise(setImmediate);
  assert.equal(f.requests.filter(request => request.op === "save").length, 1);
  assert.equal(f.controller.snapshot().dirty, false);
});

test("cancelled draft unload still permits ordinary hidden-tab autosave", async () => {
  const f = sessionFixture();
  await f.controller.load();
  f.controller.markDirty();
  let prevented = false;
  f.window.dispatch("beforeunload", { preventDefault() { prevented = true; } });
  assert.equal(prevented, true);
  f.document.visibilityState = "hidden";
  f.document.dispatch("visibilitychange");
  await new Promise(setImmediate);
  assert.equal(f.requests.filter(request => request.op === "save").length, 1);
  assert.equal(f.controller.snapshot().dirty, false);
});

test("draft session suppresses presentation-generated edits while restoring", async () => {
  const f = sessionFixture({ revision: 4, values: { positive_prompt: "saved" } });
  await f.controller.load();
  assert.equal(f.text(), "saved");
  assert.equal(f.controller.restoring, false);
  assert.equal(f.controller.snapshot().dirty, false);
  assert.equal(f.requests.length, 1);
  f.advanced.dispatch("toggle");
  assert.equal(f.controller.snapshot().dirty, false);
  f.form.dispatch("change");
  assert.equal(f.controller.snapshot().dirty, true);
  await f.controller.flush();
  assert.equal(f.requests.filter(request => request.op === "save").length, 1);
});

test("draft session validates before mutation and keeps unsupported remote work", async () => {
  const f = sessionFixture({ revision: 5, values: { model: "unavailable", positive_prompt: "remote" } });
  await f.controller.load();
  assert.deepEqual(f.applied, []);
  assert.equal(f.text(), "local prompt");
  assert.equal(f.controller.snapshot().status, "conflict");
  assert.equal(f.controller.restoring, false);
  assert.throws(() => f.controller.apply({ values: { model: "unavailable" } }), /unavailable/);
  assert.equal(await f.controller.flush(), false);
  assert.equal(f.requests.length, 1);
});

test("draft session releases restore suppression even if presentation fails", async () => {
  let f;
  f = sessionFixture(null, { apply: () => { assert.equal(f.controller.restoring, true); throw new Error("view failed"); } });
  await f.controller.load();
  assert.throws(() => f.controller.apply({ values: {} }), /view failed/);
  assert.equal(f.controller.restoring, false);
  f.controller.markDirty();
  assert.equal(f.controller.snapshot().dirty, true);
  await f.controller.flush();
});

test("failed remote draft choice retains conflict feedback and recovery controls", async () => {
  const f = sessionFixture({ revision: 5, values: { model: "unavailable", positive_prompt: "remote" } });
  await f.controller.load();
  f.document.getElementById("generation-draft-remote").dispatch("click");
  await new Promise(setImmediate);
  assert.equal(f.controller.snapshot().status, "conflict");
  assert.equal(f.document.getElementById("generation-draft-bar").dataset.state, "conflict");
  assert.equal(f.document.getElementById("generation-draft-status").textContent, "unavailable");
  assert.equal(f.document.getElementById("generation-draft-local").hidden, false);
  assert.equal(f.text(), "local prompt");
});

test("draft session saves text before a shared background upload and binds its completion next", async () => {
  const f = sessionFixture(), gate = deferred();
  let uploads = 0;
  const source = { name: "reference.png" };
  const entry = { field: "input_image", source, state: { textContent: "" }, upload: () => {
    uploads++;
    f.active(1);
    return gate.promise.then(() => { f.pending([]); f.active(0); });
  } };
  await f.controller.load();
  f.pending([entry]);
  f.controller.markDirty();
  await f.controller.flush();
  assert.equal(f.requests.at(-1).body.get("pending"), "1");
  assert.equal(entry.state.textContent, "Сохраняем материал...");
  assert.equal(uploads, 1);
  f.controller.markDirty();
  await f.controller.flush();
  assert.equal(uploads, 1);
  gate.resolve();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(f.controller.snapshot().dirty, true);
  await f.controller.flush();
  assert.equal(f.requests.at(-1).body.get("pending"), "0");
});

test("draft background failures are attempted once and cannot overwrite a replacement's status", async () => {
  const f = sessionFixture(), gate = deferred();
  let uploads = 0;
  const state = { textContent: "" };
  const entry = { field: "input_video", source: { name: "old.mp4" }, state, upload: async () => { uploads++; await gate.promise; throw new Error("old failed"); } };
  await f.controller.load();
  f.pending([entry]);
  f.controller.markDirty();
  await f.controller.flush();
  f.pending([]);
  state.textContent = "new file ready";
  gate.resolve();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(state.textContent, "new file ready");
  f.pending([entry]);
  await f.controller.flush();
  assert.equal(uploads, 1);
});

test("draft session observes relevant store changes, not progress-only updates", async () => {
  const f = sessionFixture();
  await f.controller.load();
  f.store.emit("assistant:change", { previous: { action: "" }, current: { action: "", status: "loading" } });
  assert.equal(f.controller.snapshot().dirty, false);
  f.store.emit("assistant:change", { previous: { action: "" }, current: { action: "applied" } });
  assert.equal(f.controller.snapshot().dirty, true);
  await f.controller.flush();
  f.store.emit("wizard:change", { previous: { scenarioID: "first" }, current: { scenarioID: "second" } });
  assert.equal(f.controller.snapshot().dirty, true);
  await f.controller.flush();
});

test("draft transport preserves endpoint, CSRF, cache and timeout contracts", async () => {
  const calls = [];
  let csrf = "first";
  const signal = new AbortController().signal;
  const transport = createTransport({
    csrf: () => csrf,
    timeoutSignal: milliseconds => { assert.equal(milliseconds, 20000); return signal; },
    fetcher: async (url, request) => { calls.push({ url, request }); return { ok: true, status: 200, json: async () => ({ draft: null }) }; },
  });
  await transport("load");
  const body = new URLSearchParams({ positive_prompt: "kept", draft_revision: "4" });
  await transport("save", body);
  csrf = "current";
  await transport("delete", new URLSearchParams({ draft_revision: "5" }));
  assert.deepEqual(calls.map(call => [call.url, call.request.method]), [["/generate/draft", "GET"], ["/generate/draft", "POST"], ["/generate/draft/delete", "POST"]]);
  for (const { request } of calls) {
    assert.equal(request.cache, "no-store");
    assert.equal(request.credentials, "same-origin");
    assert.equal(request.signal, signal);
  }
  assert.equal(calls[1].request.body.get("csrf"), "first");
  assert.equal(calls[1].request.body.get("positive_prompt"), "kept");
  assert.equal(calls[2].request.body.get("csrf"), "current");
  await assert.rejects(transport("invalid", body), /Unknown/);
  assert.equal(calls.length, 3);
});

test("draft transport never retries failed mutations or replaces conflict bodies", async () => {
  let calls = 0;
  const draft = { revision: 7, values: { positive_prompt: "remote" } };
  const transport = createTransport({
    csrf: () => "csrf", timeoutSignal: () => undefined,
    fetcher: async () => { calls++; return { ok: false, status: 409, json: async () => ({ draft }) }; },
  });
  const response = await transport("save", new URLSearchParams());
  assert.deepEqual(response, { ok: false, status: 409, payload: { draft } });
  assert.equal(calls, 1);
  const failed = createTransport({
    csrf: () => "csrf", timeoutSignal: () => undefined,
    fetcher: async () => { calls++; throw new Error("offline"); },
  });
  await assert.rejects(failed("save", new URLSearchParams()), /offline/);
  assert.equal(calls, 2);
});
