const test = require("node:test");
const assert = require("node:assert/strict");
const { createController, createPersistence, createTransport, newID } = require("../../static/generation-submission.js");

const requestID = "synthetic-request-0001", jobID = "synthetic-job-0001", batchID = "synthetic-batch-0001", promptID = "synthetic-prompt-0001";
const pointer = (overrides = {}) => ({ requestID, kind: "single", savedAt: 1000, ...overrides });
const reply = (payload, status = 200) => ({ ok: status >= 200 && status < 300, status, payload });
const job = (state = "running", extra = {}) => reply({ request_id: requestID, job_id: jobID, prompt_id: promptID, state, ...extra });
const batch = (state = "queued", extra = {}) => reply({ request_id: requestID, kind: "batch", batch: { batch_id: batchID, state }, ...extra });
const closed = () => reply({ request_id: requestID, kind: "single", state: "cancelled", request_closed: true });
const memory = () => {
  const map = new Map();
  return { map, get length() { return map.size; }, key: index => [...map.keys()][index],
    getItem: key => map.get(key) ?? null, setItem: (key, value) => map.set(key, value), removeItem: key => map.delete(key) };
};
const flush = async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); };
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; };
const fixture = (options = {}) => {
  const storage = options.storage || memory(), tasks = new Map(), calls = [], states = [], accepted = [], finished = [], progress = [], sockets = [];
  const responses = [], persistence = options.persistence || createPersistence({ storage: () => storage, owner: "1" });
  let clock = 1000, timerID = 0;
  const controller = createController({ persistence, newRequestID: () => requestID,
    now: () => clock, schedule: (fn, delay) => { tasks.set(++timerID, { fn, at: clock + delay }); return timerID; }, unschedule: id => tasks.delete(id),
    transport: async (operation, record, body, signal) => {
      calls.push({ operation, record: { ...record }, body, signal });
      const response = responses.shift();
      if (response instanceof Error) throw response;
      return typeof response === "function" ? response(operation, record) : response || reply({ state: "unknown" }, 404);
    },
    socket: () => { const socket = { close() { this.closed = true; this.onclose?.(); } }; sockets.push(socket); return socket; },
    onState: state => states.push(state), onAccepted: (...args) => accepted.push(args), onFinished: (...args) => finished.push(args), onProgress: event => progress.push(event),
  });
  const advance = async milliseconds => {
    const end = clock + milliseconds;
    for (let i = 0; i < 1000; i++) {
      const next = [...tasks].filter(([, task]) => task.at <= end).sort((a, b) => a[1].at - b[1].at)[0];
      if (!next) { clock = end; await flush(); return; }
      const [id, task] = next; clock = Math.max(clock, task.at); tasks.delete(id); task.fn(); await flush();
    }
    throw new Error("unbounded timer loop");
  };
  return { controller, storage, persistence, calls, responses, states, accepted, finished, progress, sockets, tasks, advance,
    jump: milliseconds => { clock += milliseconds; }, operations: () => calls.map(call => call.operation) };
};

test("pointers are account-scoped and never contain launch parameters or media", () => {
  const storage = memory(), owner1 = createPersistence({ storage: () => storage, owner: 1 }), owner2 = createPersistence({ storage: () => storage, owner: 2 });
  owner1.write(pointer({ prompt: "private prompt", media: "private image", csrf: "private token" }));
  assert.equal(owner2.read(), null);
  assert.equal(owner1.read().requestID, requestID);
  assert.doesNotMatch(JSON.stringify([...storage.map]), /private/);
  assert.throws(() => createPersistence({ storage: () => storage, owner: "" }));
});

test("multiple tab requests stay independent and the oldest has no expiry", () => {
  const f = fixture();
  f.persistence.write(pointer({ requestID: "synthetic-request-0002", savedAt: 2000 }));
  f.persistence.write(pointer());
  assert.equal(f.persistence.read().requestID, requestID);
  f.persistence.complete(pointer());
  assert.equal(f.persistence.read().requestID, "synthetic-request-0002");
});

test("stale tab writes cannot erase cancellation or resurrect terminal pointers", () => {
  const f = fixture(), stale = f.persistence.write(pointer());
  f.persistence.write({ ...stale, cancelling: true });
  assert.equal(f.persistence.write(stale).cancelling, true);
  f.persistence.complete(stale);
  assert.equal(f.persistence.write(stale).terminal, true);
  assert.equal(f.persistence.read(), null);
});

test("separate monotonic flags survive an overlapping stale pointer write", () => {
  const f = fixture(), stale = f.persistence.write(pointer());
  f.persistence.complete(stale);
  f.storage.setItem(`ai-gateway.generation-submission.1.${requestID}`, JSON.stringify(stale));
  assert.equal(f.persistence.read(), null);
});

test("legacy pointer is recoverable and a terminal marker survives failed cleanup", () => {
  const f = fixture();
  f.storage.setItem("ai-gateway.active-generation", JSON.stringify(pointer()));
  assert.equal(f.persistence.read().requestID, requestID);
  f.storage.removeItem = () => { throw new Error("deletion disabled"); };
  f.persistence.complete(pointer());
  assert.equal(f.persistence.read(), null);
});

test("completed pointers compact to a terminal marker without job or prompt IDs", () => {
  const f = fixture(), record = f.persistence.write(pointer({ jobID, promptID }));
  f.persistence.complete(record);
  assert.deepEqual([...f.storage.map], [[`ai-gateway.generation-submission.1.${requestID}.terminal`, "1"]]);
  assert.equal(f.persistence.write(record).terminal, true);
  assert.equal(f.storage.length, 1);
});

test("closing an unconfirmed legacy pointer does not erase another account's recovery", async () => {
  const f = fixture();
  f.storage.setItem("ai-gateway.active-generation", JSON.stringify(pointer({ jobID, promptID })));
  f.responses.push(reply({ request_id: requestID, kind: "single", state: "unknown", request_closed: false }, 404), closed());
  f.controller.resume(); await flush(); f.controller.cancel(); await flush();
  assert.deepEqual(f.operations(), ["lookup", "close"]);
  assert.equal(f.persistence.read(), null);
  const other = createPersistence({ storage: () => f.storage, owner: "2" });
  assert.equal(other.read().requestID, requestID);
});

test("an owned completed binding retires its legacy pointer after confirmation", async () => {
  const f = fixture();
  f.storage.setItem("ai-gateway.active-generation", JSON.stringify(pointer({ jobID, promptID })));
  f.responses.push(job("completed", { outputs: [{}] }));
  f.controller.resume(); await flush();
  assert.equal(f.storage.getItem("ai-gateway.active-generation"), null);
  assert.equal(f.persistence.read(), null);
});

test("corrupt or mismatched saved identity is fail-closed", () => {
  const f = fixture();
  for (const value of [{}, pointer({ jobID: "short" }), pointer({ kind: "other" }), pointer({ savedAt: NaN })]) assert.throws(() => f.persistence.write(value));
  f.persistence.write(pointer({ jobID }));
  assert.throws(() => f.persistence.write(pointer({ jobID: "synthetic-job-0002" })));
  f.storage.setItem(`ai-gateway.generation-submission.1.${requestID}`, "{");
  assert.equal(f.controller.resume(), false);
  assert.equal(f.controller.snapshot().phase, "storage_error");
  assert.equal(f.calls.length, 0);
});

test("request IDs require secure randomness", () => {
  assert.equal(newID({ randomUUID: () => requestID }), requestID);
  assert.equal(newID({ getRandomValues: array => array.fill(10) }), "0a".repeat(16));
  assert.throws(() => newID({}));
});

test("start persists its identity before exactly one POST and blocks duplicate clicks", async () => {
  const f = fixture(), pending = deferred();
  f.responses.push(() => { assert.equal(f.persistence.read().requestID, requestID); return pending.promise; });
  const starting = f.controller.start(new URLSearchParams({ prompt: "synthetic" }));
  await assert.rejects(f.controller.start("prompt=duplicate"));
  pending.resolve(job()); await starting;
  assert.deepEqual(f.operations(), ["submit"]);
  assert.equal(f.controller.snapshot().record.jobID, jobID);
  assert.equal(f.accepted.length, 1);
});

test("unavailable persistence prevents submission", async () => {
  const f = fixture(); f.storage.setItem = () => { throw new Error("quota"); };
  await assert.rejects(f.controller.start("prompt=synthetic"));
  assert.equal(f.controller.snapshot().phase, "storage_error");
  assert.equal(f.calls.length, 0);
});

test("existing saved work is resumed instead of submitting a new form", async () => {
  const f = fixture(); f.persistence.write(pointer()); f.responses.push(job());
  assert.equal(await f.controller.start("prompt=changed"), false); await flush();
  assert.deepEqual(f.operations(), ["lookup"]);
});

test("lost submit response recovers by lookup without another launch", async () => {
  const f = fixture(); f.responses.push(new Error("lost response"), reply({ state: "unknown" }, 404), job(), job("completed", { outputs: [{ filename: "synthetic.png" }] }));
  await f.controller.start("prompt=synthetic");
  await f.advance(2000); assert.equal(f.controller.snapshot().pending, true);
  await f.advance(4000); await f.advance(2000);
  assert.deepEqual(f.operations(), ["submit", "lookup", "lookup", "status"]);
  assert.equal(f.finished.length, 1);
  assert.equal(f.persistence.read(), null);
});

test("reload after a lost POST uses the same identity and no launch replay", async () => {
  const first = fixture(); first.responses.push(new Error("lost")); await first.controller.start("prompt=synthetic"); first.controller.suspend();
  const second = fixture({ storage: first.storage }); second.responses.push(job()); second.controller.resume(); await flush();
  assert.deepEqual(second.operations(), ["lookup"]);
  assert.equal(second.calls[0].record.requestID, requestID);
});

test("lost batch response survives reload and hands off the bound batch", async () => {
  const first = fixture(); first.responses.push(new Error("lost")); await first.controller.start("batch_count=2", { kind: "batch" }); first.controller.suspend();
  const second = fixture({ storage: first.storage }); second.responses.push(batch()); second.controller.resume(); await flush();
  assert.deepEqual(second.operations(), ["lookup"]);
  assert.equal(second.calls[0].record.kind, "batch"); assert.equal(second.finished[0][2], "batch_accepted");
});

test("a cancellation recorded by another tab is retained by the next accepted response", async () => {
  const f = fixture(); f.responses.push(job()); await f.controller.start("prompt=synthetic");
  f.persistence.write({ ...f.persistence.read(), cancelling: true });
  f.responses.push(job(), reply({ job: { job_id: jobID, state: "cancelled" } }));
  await f.advance(2000); assert.equal(f.controller.snapshot().phase, "cancelling"); await f.advance(2000);
  assert.deepEqual(f.operations(), ["submit", "status", "cancel"]);
  assert.equal(f.finished[0][0].state, "cancelled");
});

test("unknown cancellation closes creation before treating the request as stopped", async () => {
  const f = fixture(); f.responses.push(new Error("lost"), new Error("lost close"), closed());
  await f.controller.start("prompt=synthetic"); f.controller.cancel(); await flush();
  assert.equal(f.controller.snapshot().pending, true);
  assert.equal(f.persistence.read().cancelling, true);
  await f.advance(4000);
  assert.deepEqual(f.operations(), ["submit", "close", "close"]);
  assert.equal(f.finished[0][0].state, "cancelled");
});

test("closure finding a running job proceeds through job cancellation confirmation", async () => {
  const f = fixture(); f.responses.push(new Error("lost"), job(), reply({ job: { job_id: jobID, request_id: requestID, state: "cancelling" } }, 202), reply({ job: { job_id: jobID, request_id: requestID, state: "cancelled" } }));
  await f.controller.start("prompt=synthetic"); f.controller.cancel(); await flush(); await f.advance(0);
  assert.equal(f.controller.snapshot().pending, true);
  await f.advance(2000);
  assert.deepEqual(f.operations(), ["submit", "close", "cancel", "cancel"]);
  assert.equal(f.finished[0][0].state, "cancelled");
});

test("late original POST cannot overwrite the cancellation result", async () => {
  const f = fixture(), pending = deferred(); f.responses.push(() => pending.promise, closed());
  const launching = f.controller.start("prompt=synthetic");
  const signal = f.calls[0].signal; f.controller.cancel(); await flush();
  assert.equal(signal.aborted, true);
  pending.resolve(job()); await launching;
  assert.equal(f.finished.length, 1); assert.equal(f.finished[0][0].state, "cancelled");
  assert.equal(f.accepted.length, 0); assert.equal(f.controller.snapshot().pending, false);
});

for (const status of [400, 409, 422, 429]) test(`rejection ${status} is fenced before a fresh attempt is offered`, async () => {
  const f = fixture(); f.responses.push(reply({ error: "synthetic rejection" }, status), closed());
  await f.controller.start("prompt=synthetic");
  assert.equal(f.persistence.read().resolvingRejection, true);
  await f.advance(0);
  assert.deepEqual(f.operations(), ["submit", "close"]);
  assert.equal(f.finished[0][0].state, "error");
  assert.match(f.finished[0][0].message, /synthetic rejection/);
});

test("rejection closure finding a bound job monitors it without user cancellation", async () => {
  const f = fixture(); f.responses.push(reply({ error: "conflict" }, 409), job(), job("completed", { outputs: [{ filename: "synthetic.png" }] }));
  await f.controller.start("prompt=synthetic"); await f.advance(0); await f.advance(2000);
  assert.deepEqual(f.operations(), ["submit", "close", "status"]);
  assert.equal(f.finished[0][0].state, "completed");
});

for (const status of [401, 403]) test(`authentication ${status} pauses with a durable pointer and manual recovery`, async () => {
  const f = fixture(); f.responses.push(reply({}, status)); await f.controller.start("prompt=synthetic");
  assert.equal(f.controller.snapshot().phase, "auth_required"); await f.advance(60000);
  assert.equal(f.calls.length, 1); assert.equal(f.persistence.read().requestID, requestID);
  f.responses.push(job()); f.controller.refresh(); await flush();
  assert.deepEqual(f.operations(), ["submit", "lookup"]);
});

for (const mismatch of [{ request_id: "synthetic-request-9999" }, { kind: "batch" }, { job_id: "short" }, { state: "invented" }, { state: "partial" }, { job: { job_id: "synthetic-job-9999", media: [{}] } }]) test(`malformed response is not accepted: ${JSON.stringify(mismatch)}`, async () => {
  const f = fixture(); f.responses.push(job("running", mismatch)); await f.controller.start("prompt=synthetic");
  assert.equal(f.controller.snapshot().phase, "recovering"); assert.equal(f.finished.length, 0); assert.equal(f.accepted.length, 0);
  assert.equal(f.persistence.read().requestID, requestID);
});

test("batch envelope cannot contradict its nested state", async () => {
  const f = fixture(); f.responses.push(batch("running", { state: "cancelled" }));
  await f.controller.start("batch_count=2", { kind: "batch" });
  assert.equal(f.controller.snapshot().phase, "recovering"); assert.equal(f.finished.length, 0);
});

test("status prompt identity mismatch never replaces the accepted job", async () => {
  const f = fixture(); f.responses.push(job(), job("completed", { prompt_id: "foreign-prompt", outputs: [{}] }));
  await f.controller.start("prompt=synthetic"); await f.advance(2000);
  assert.equal(f.finished.length, 0); assert.equal(f.controller.snapshot().record.promptID, promptID);
});

test("terminal status without outputs retrieves retained media from request lookup", async () => {
  const f = fixture(); f.responses.push(job(), job("completed"), job("completed", { job: { job_id: jobID, media: [{ url: "/synthetic.png" }] } }));
  await f.controller.start("prompt=synthetic"); await f.advance(2000);
  assert.equal(f.finished.length, 0); await f.advance(2000);
  assert.deepEqual(f.operations(), ["submit", "status", "lookup"]);
  assert.equal(f.finished[0][0].job.media.length, 1);
});

test("completion found during cancellation retrieves retained result instead of losing it", async () => {
  const f = fixture(); f.responses.push(job(), job(), reply({ job: { job_id: jobID, state: "completed" } }), job("completed", { job: { job_id: jobID, media: [{}] } }));
  await f.controller.start("prompt=synthetic"); f.controller.cancel(); await flush(); await f.advance(2000); await f.advance(2000);
  assert.deepEqual(f.operations(), ["submit", "lookup", "cancel", "lookup"]);
  assert.equal(f.finished[0][0].job.media.length, 1);
});

test("expired retained receipt resolves a cancellation after job history is pruned", async () => {
  const f = fixture(); f.responses.push(job(), reply({ request_id: requestID, kind: "single", state: "expired", request_closed: true }, 410));
  await f.controller.start("prompt=synthetic"); f.controller.cancel(); await flush();
  assert.deepEqual(f.operations(), ["submit", "lookup"]);
  assert.equal(f.finished[0][0].state, "expired");
});

for (const state of ["queued", "partial", "completed", "failed"]) test(`accepted batch ${state} hands off durably without per-child launch`, async () => {
  const f = fixture(); f.responses.push(batch(state)); await f.controller.start("batch_count=2", { kind: "batch" });
  assert.deepEqual(f.operations(), ["submit"]); assert.equal(f.finished[0][2], "batch_accepted");
  assert.equal(f.finished[0][0].batch.batch_id, batchID); assert.equal(f.persistence.read(), null);
});

test("resumed batch cancellation waits through partial completion", async () => {
  const f = fixture(); f.persistence.write(pointer({ kind: "batch", batchID, confirmed: true, cancelling: true }));
  f.responses.push(batch("cancelling"), reply({ batch: { batch_id: batchID, state: "partial" } }));
  f.controller.resume(); await flush(); await f.advance(2000);
  assert.deepEqual(f.operations(), ["lookup", "cancel"]); assert.equal(f.finished[0][0].state, "partial");
});

test("suspend aborts in-flight read and ignores its late result until resume", async () => {
  const f = fixture(), pending = deferred(); f.persistence.write(pointer()); f.responses.push(() => pending.promise);
  f.controller.resume(); const signal = f.calls[0].signal; f.controller.suspend();
  assert.equal(signal.aborted, true); pending.resolve(job("completed")); await flush();
  assert.equal(f.finished.length, 0); assert.equal(f.tasks.size, 0);
  f.responses.push(job()); f.controller.resume(); await flush(); assert.deepEqual(f.operations(), ["lookup", "lookup"]);
});

test("hidden pages close live socket and slow polling; visibility resumes reads", async () => {
  const f = fixture(); f.responses.push(job()); await f.controller.start("prompt=synthetic");
  f.controller.setVisible(false); assert.equal(f.sockets[0].closed, true);
  await f.advance(29000); assert.equal(f.calls.length, 1);
  f.responses.push(job()); f.controller.setVisible(true); await f.advance(0);
  assert.deepEqual(f.operations(), ["submit", "status"]); assert.equal(f.sockets.length, 2);
});

test("only the current prompt can publish live progress and old sockets are detached", async () => {
  const f = fixture(); f.responses.push(job()); await f.controller.start("prompt=synthetic");
  const socket = f.sockets[0];
  for (const event of [{ type: "progress", data: { prompt_id: "foreign" } }, { type: "progress", data: {} }, { type: "other", data: { prompt_id: promptID } }]) socket.onmessage({ data: JSON.stringify(event) });
  socket.onmessage({ data: "{" }); assert.equal(f.progress.length, 0);
  socket.onmessage({ data: JSON.stringify({ type: "progress", data: { prompt_id: promptID, value: 1 } }) });
  assert.equal(f.progress.length, 1); assert.equal(f.controller.snapshot().live, true);
  f.controller.suspend(); assert.equal(socket.onmessage, null); assert.equal(f.controller.snapshot().live, false);
});

test("socket reconnect has backoff and read fallback remains active", async () => {
  const f = fixture(); f.responses.push(job(), job()); await f.controller.start("prompt=synthetic");
  f.sockets[0].onerror(); assert.equal(f.controller.snapshot().live, false);
  await f.advance(1000); assert.equal(f.sockets.length, 1); await f.advance(1000);
  assert.equal(f.sockets.length, 2); assert.deepEqual(f.operations(), ["submit", "status"]);
});

test("long-running work pauses without dropping identity and refresh is read-only", async () => {
  const f = fixture(); f.responses.push(job()); await f.controller.start("prompt=synthetic");
  f.jump(21 * 60 * 1000); await f.advance(0);
  assert.equal(f.controller.snapshot().phase, "paused"); assert.equal(f.persistence.read().requestID, requestID);
  f.responses.push(job()); f.controller.refresh(); await flush(); assert.deepEqual(f.operations(), ["submit", "lookup"]);
});

test("failure to persist a confirmed outcome retains recovery and does not enable launch", async () => {
  const f = fixture(); f.responses.push(job()); await f.controller.start("prompt=synthetic");
  f.responses.push(job("completed", { outputs: [{}] }));
  const original = f.storage.setItem;
  f.storage.setItem = (key, value) => { if (key.endsWith(".terminal")) throw new Error("quota"); original(key, value); };
  await f.advance(2000); assert.equal(f.controller.snapshot().phase, "storage_error"); assert.equal(f.finished.length, 0);
  await assert.rejects(f.controller.start("prompt=again"));
  f.storage.setItem = original; f.responses.push(job("completed", { outputs: [{}] })); f.controller.refresh(); await flush();
  assert.equal(f.finished.length, 1);
});

test("HTTP adapter uses the existing endpoints, fresh CSRF and one copied launch body", async () => {
  const calls = [], source = new URLSearchParams({ csrf: "old", client_request_id: "old", prompt: "synthetic" });
  const transport = createTransport({ csrf: () => "fresh", timeoutSignal: () => new AbortController().signal,
    fetcher: async (path, options) => { calls.push({ path, ...options }); return { ok: true, status: 200, json: async () => ({}) }; } });
  const record = pointer({ jobID, promptID, batchID }), signal = new AbortController().signal;
  for (const operation of ["submit", "lookup", "status", "close", "cancel"]) await transport(operation, record, source, signal, "correlation");
  await transport("submit", { ...record, kind: "batch" }, source, signal);
  await transport("cancel", { ...record, kind: "batch" }, undefined, signal);
  assert.deepEqual(calls.map(call => call.path.split("?")[0]), ["/generate/run", "/generate/submission", "/generate/status", "/generate/submission/close", "/generate/jobs/cancel", "/generate/batches", "/generate/batches/cancel"]);
  assert.equal(source.get("csrf"), "old"); assert.equal(source.get("client_request_id"), "old");
  assert.equal(calls[0].body.get("csrf"), "fresh"); assert.equal(calls[0].body.get("client_request_id"), requestID);
  assert.equal(calls[0].headers["X-Correlation-ID"], "correlation");
  assert.equal(calls[4].body.get("job_id"), jobID); assert.equal(calls[6].body.get("batch_id"), batchID);
  assert.equal(calls[1].body, undefined); assert.equal(calls[2].body, undefined);
  for (const call of calls) { assert.equal(call.credentials, "same-origin"); assert.equal(call.redirect, "error"); assert.equal(call.cache, "no-store"); }
});

test("HTTP adapter exposes uncertain non-JSON replies for read-only recovery", async () => {
  const transport = createTransport({ csrf: () => "fresh", timeoutSignal: () => new AbortController().signal,
    fetcher: async () => ({ ok: false, status: 502, json: async () => { throw new Error("invalid JSON"); } }) });
  assert.deepEqual(await transport("lookup", pointer(), undefined, new AbortController().signal), reply({}, 502));
});

test("HTTP adapter combines page-lifecycle and timeout cancellation", async () => {
  for (const cancellation of ["page", "timeout"]) {
    const page = new AbortController(), timeout = new AbortController(); let received;
    const transport = createTransport({ csrf: () => "fresh", timeoutSignal: ms => { assert.equal(ms, 20000); return timeout.signal; },
      fetcher: async (_, { signal }) => { received = signal; return { ok: true, status: 200, json: async () => ({}) }; } });
    await transport("lookup", pointer(), undefined, page.signal);
    (cancellation === "page" ? page : timeout).abort(); assert.equal(received.aborted, true);
  }
});
