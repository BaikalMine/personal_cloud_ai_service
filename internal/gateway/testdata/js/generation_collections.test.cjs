const assert = require("node:assert/strict");
const test = require("node:test");
const { createController, createTransport } = require("../../static/generation-collections.js");
const record = (id = "1", revision = 1) => ({ id, name: `Series ${id}`, revision, has_context: true, values: { positive_prompt: "saved" }, assets: [] });
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; };

test("collection selection fences old and hidden reads", async () => {
  const first = deferred(), second = deferred();
  const c = createController({ transport: (_, body) => body.collection_id === "1" ? first.promise : second.promise });
  const a = c.select("1"), b = c.select("2");
  second.resolve({ collection: record("2") }); await b;
  first.resolve({ collection: record("1") }); await a;
  assert.equal(c.snapshot().record.id, "2");
  const wait = deferred();
  const hidden = createController({ transport: () => wait.promise });
  const read = hidden.select("1"); hidden.setActive(false);
  wait.resolve({ collection: record() }); await read;
  assert.equal(hidden.snapshot().record, null);
});

test("collection writes use the inspected version and serialize mutations", async () => {
  const pending = deferred(), calls = [];
  const c = createController({ transport: (op, body) => { calls.push({ op, body }); return op === "read" ? Promise.resolve({ collection: record() }) : pending.promise; } });
  await c.select("1"); const expected = c.snapshot().record;
  const saving = c.write("rename", { name: "Renamed" }, expected);
  await assert.rejects(c.write("rename", { name: "Again" }, expected));
  assert.equal(calls.length, 2); assert.equal(calls[1].body.collection_revision, 1);
  pending.resolve({ collection: { ...record("1", 2), name: "Renamed" } }); await saving;
  assert.equal(c.snapshot().record.name, "Renamed");
  await assert.rejects(c.write("rename", { name: "Old" }, expected));
  assert.equal(calls.length, 2);
});

test("unknown collection mutation must be refreshed before another POST", async () => {
  let writes = 0, reads = 0;
  const c = createController({ transport: async op => { if (op === "read") return { collection: record("1", ++reads) }; writes++; throw Object.assign(new Error("unknown"), { uncertain: true }); } });
  await c.select("1");
  await assert.rejects(c.write("save_draft", { draft_revision: 9 }));
  assert.equal(c.snapshot().uncertain, true);
  await assert.rejects(c.write("save_draft", { draft_revision: 9 })); assert.equal(writes, 1);
  await c.refresh(); assert.equal(c.snapshot().uncertain, false); assert.equal(c.snapshot().record.revision, 2);
});

test("foreign or malformed collection responses cannot expose prior context", async () => {
  let payload = { collection: record() };
  const c = createController({ transport: async () => payload });
  await c.select("1");
  payload = { collection: record("9") }; await c.refresh();
  assert.equal(c.snapshot().status, "error"); assert.equal(c.snapshot().record, null);
});

test("collection transport is private, uses CSRF and never retries an unknown POST", async () => {
  const requests = [];
  const transport = createTransport({ csrf: () => "token", timeout: () => undefined, fetcher: async (url, options) => { requests.push({ url, options }); throw new Error("lost response"); } });
  await assert.rejects(transport("rename", { collection_id: "1", collection_revision: 3, name: "Series" }), error => error.uncertain);
  assert.equal(requests.length, 1);
  assert.equal(requests[0].options.body.get("csrf"), "token");
  assert.equal(requests[0].options.body.get("action"), "rename");
  assert.equal(requests[0].options.cache, "no-store");
  assert.equal(requests[0].options.credentials, "same-origin");
});

test("collection access loss is distinct from uncertain mutation", async () => {
  const transport = createTransport({ csrf: () => "token", timeout: () => undefined, fetcher: async () => ({ status: 403, ok: false }) });
  await assert.rejects(transport("open", { collection_id: "1" }), error => error.status === 403 && !error.uncertain);
});

test("collection failures require explicit refresh and clear inaccessible context", async () => {
  let reads = 0;
  const c = createController({ transport: async op => {
    if (op === "read") { reads++; return { collection: record() }; }
    throw Object.assign(new Error("missing"), { status: 404 });
  } });
  await c.select("1"); await assert.rejects(c.write("rename", { name: "Other" }));
  assert.equal(c.snapshot().record, null);
  await c.select("1"); assert.equal(reads, 1);
  await c.refresh(); assert.equal(reads, 2);
});

test("resume during pending mutation refreshes only after the acknowledgement", async () => {
  const pending = deferred(); let reads = 0; const seen = [];
  const c = createController({ transport: async op => op === "read" ? { collection: record("1", ++reads) } : pending.promise, onRecord: value => seen.push(value.revision) });
  await c.select("1"); const save = c.write("rename", { name: "Next" });
  c.setActive(false); c.setActive(true);
  assert.equal(c.snapshot().record, null); assert.equal(reads, 1);
  pending.resolve({ collection: record("1", 99) }); await save;
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(reads, 2); assert.equal(c.snapshot().record.revision, 2);
  assert.deepEqual(seen, [1, 2]);
});
