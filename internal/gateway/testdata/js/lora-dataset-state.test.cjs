const test = require("node:test");
const assert = require("node:assert/strict");
const { createController } = require("../../static/lora-dataset-state.js");

const copy = (value) => JSON.parse(JSON.stringify(value));
const defaults = { version: 1, settings: { name: "", trigger_word: "" }, images: [] };
function fixture() {
  let sequence = 0;
  const sets = new Map();
  const calls = [];
  const server = async (path, method = "GET", body) => {
    calls.push({ path, method, body: body && copy(body) });
    if (path === "" && method === "GET") return { datasets: [...sets.values()].map((view) => copy(view.dataset)) };
    if (path === "" && method === "POST") {
      const id = body.client_id;
      if (!sets.has(id)) sets.set(id, { dataset: { id, revision: ++sequence }, manifest: copy(body.manifest), assets: {} });
      return copy(sets.get(id));
    }
    const [, id, action] = path.split("/");
    const view = sets.get(id);
    if (method === "GET") return copy(view);
    if (action === "save") {
      if (view.dataset.revision !== body.revision) throw Object.assign(new Error("changed elsewhere"), { status: 409 });
      view.manifest = copy(body.manifest); view.dataset.revision = ++sequence;
      return copy(view);
    }
    throw new Error(`Unexpected ${method} ${path}`);
  };
  let counter = 0;
  const create = (request = server) => createController({ request, defaults, schedule: () => 1, unschedule: () => {}, newID: () => `new-${++counter}` });
  return { create, server, sets, calls };
}
const deferred = () => { let resolve; const promise = new Promise((done) => { resolve = done; }); return { promise, resolve }; };

test("dataset persists exact captions, order and excluded items across reload", async () => {
  const f = fixture(); const c = f.create(); await c.load();
  c.state.manifest.settings = { name: "Portrait", trigger_word: "person_x" };
  c.state.manifest.images = [{ id: "b", asset_id: "asset-b", caption: "  manual\n", excluded: true }, { id: "a", asset_id: "asset-a", caption: "light hair", excluded: false }];
  c.touch(); assert.equal(await c.flush(), true);
  const fresh = f.create(); await fresh.load();
  assert.deepEqual(fresh.state.manifest, c.state.manifest);
  assert.equal(fresh.state.status, "saved");
  assert.deepEqual(defaults.images, []);
});

test("pending uploads stay local until their asset is available", async () => {
  const f = fixture(); const c = f.create(); await c.load();
  c.state.manifest.images = [{ id: "local", asset_id: "", caption: "keep this" }]; c.touch(); await c.flush();
  assert.equal(f.sets.get(c.state.dataset.id).manifest.images.length, 0);
  assert.equal(c.state.manifest.images[0].caption, "keep this");
  c.state.manifest.images[0].asset_id = "uploaded"; c.touch(); await c.flush();
  assert.equal(f.sets.get(c.state.dataset.id).manifest.images[0].caption, "keep this");
});

test("manual edits made during an in-flight save are flushed afterward", async () => {
  const f = fixture(); const entered = deferred(); const release = deferred(); let pause = true;
  const c = f.create(async (...args) => {
    if (args[0].endsWith("/save") && pause) { pause = false; entered.resolve(); await release.promise; }
    return f.server(...args);
  });
  await c.load(); c.state.manifest.settings.name = "first"; c.touch();
  const saving = c.flush(); await entered.promise;
  c.state.manifest.settings.name = "last manual edit"; c.touch();
  const joined = c.flush(); release.resolve();
  assert.equal(await saving, true); assert.equal(await joined, true);
  assert.equal(f.sets.get(c.state.dataset.id).manifest.settings.name, "last manual edit");
  assert.equal(f.calls.filter((call) => call.path.endsWith("/save")).length, 2);
});

test("lost create response retries the same identity without duplicating the set", async () => {
  const f = fixture(); let fail = true;
  const c = f.create(async (...args) => {
    const result = await f.server(...args);
    if (args[0] === "" && args[1] === "POST" && fail) { fail = false; throw new Error("connection lost"); }
    return result;
  });
  await c.load(); c.state.manifest.settings.name = "retained"; c.touch();
  assert.equal(await c.flush(), false); assert.equal(c.state.dirty, true);
  assert.equal(await c.flush(), true); assert.equal(f.sets.size, 1);
  const created = f.calls.filter((call) => call.path === "" && call.method === "POST");
  assert.equal(created[0].body.client_id, created[1].body.client_id);
  assert.equal(c.state.manifest.settings.name, "retained");
});

test("two-tab conflict retains local edits and can save a separate copy", async () => {
  const f = fixture(); const first = f.create(); await first.load(); first.touch(); await first.flush();
  const second = f.create(); await second.load();
  first.state.manifest.settings.name = "remote"; first.touch(); await first.flush();
  second.state.manifest.settings.name = "local"; second.touch();
  assert.equal(await second.flush(), false); assert.equal(second.state.status, "conflict");
  assert.equal(await second.flush(), false); assert.equal(second.state.manifest.settings.name, "local");
  second.startNew({ preserve: true }); await second.flush();
  assert.equal(f.sets.size, 2); assert.notEqual(second.state.dataset.id, first.state.dataset.id);
  assert.equal(f.sets.get(first.state.dataset.id).manifest.settings.name, "remote");
  assert.equal(f.sets.get(second.state.dataset.id).manifest.settings.name, "local");
});

test("failed initial load is not editable until the explicit retry succeeds", async () => {
  const f = fixture(); let fail = true;
  const c = f.create(async (...args) => { if (fail) throw new Error("offline"); return f.server(...args); });
  assert.equal(await c.load(), false); assert.equal(c.state.ready, false);
  assert.equal(await c.flush(), false); assert.equal(f.sets.size, 0);
  fail = false; assert.equal(await c.load(), true); assert.equal(c.state.ready, true);
  c.touch(); assert.equal(await c.flush(), true);
});

test("new empty set resets settings without mutating a saved set", async () => {
  const f = fixture(); const c = f.create(); await c.load(); c.state.manifest.settings.name = "saved"; c.touch(); await c.flush();
  const previous = c.state.dataset.id; c.startNew();
  assert.deepEqual(c.state.manifest, defaults); assert.equal(c.state.dataset, null);
  c.touch(); await c.flush(); assert.notEqual(c.state.dataset.id, previous);
  assert.equal(f.sets.get(previous).manifest.settings.name, "saved");
});
