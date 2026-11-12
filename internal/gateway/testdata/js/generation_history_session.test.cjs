const test = require("node:test");
const assert = require("node:assert/strict");
const history = require("../../static/generation-history.js");
const job = require("../../static/generation-job.js");
const batch = require("../../static/generation-batch.js");
const tick = () => new Promise(resolve => setImmediate(resolve));
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; };
const ok = payload => ({ ok: true, status: 200, payload });
const fail = (status = 503, error = "Test failure") => ({ ok: false, status, payload: { error } });
const item = (id = "job-1", extra = {}) => ({ job_id: id, state: "completed", template_id: "text-to-image", retryable: true, ...extra });
function fixture(options = {}) {
  const slice = module => { let state = module.createState(); return { get: () => state, dispatch: action => state = module.reduce(state, action) }; };
  const slices = { job: slice(job), batch: slice(batch), history: slice(history) }, calls = [], snapshots = [], notices = [], repeats = [], polls = [];
  const stream = { connects: 0, closes: 0, connect() { this.connects++; }, close() { this.closes++; } };
  const storage = new Map(), target = { getItem: key => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value) };
  const f = { slices, calls, snapshots, notices, repeats, polls, stream, storage, target, revoked: 0, variants: [], variantErrors: [],
    jobs: [item()], batches: [], revision: 12, detail: { job: item(), transitions: [{ state: "completed", message: "Ready" }] } };
  const controller = history.createController({ ...slices, batchAPI: batch, storage: () => target,
    transport: async (operation, fields, signal) => {
      calls.push({ operation, fields, signal });
      if (options.transport) { const response = await options.transport(operation, fields, signal, f); if (response) return response; }
      if (operation === "jobs") return ok({ jobs: f.jobs, revision: f.revision });
      if (operation === "batches") return ok({ batches: f.batches });
      if (operation === "variants") return ok({ variants: [] });
      if (operation === "detail") return ok(f.detail);
      return ok({ message: "Accepted" });
    },
    live: { create: options => { f.events = options; return stream; }, poll: fn => { const poll = { fn, starts: 0, stops: 0, start() { this.starts++; }, stop() { this.stops++; } }; polls.push(poll); return poll; } },
    onState: state => snapshots.push(state), onVariants: values => f.variants.push(values), onVariantsError: error => f.variantErrors.push(error),
    onNotice: (...args) => notices.push(args), onRepeat: (...args) => repeats.push(args), onRevoked: () => f.revoked++,
    requestedJobID: options.requestedJobID, requestedBatchID: options.requestedBatchID, cancelActive: options.cancelActive, branchEditor: options.branchEditor,
  });
  f.controller = controller; return f;
}

test("an older batch deep link hydrates through task details and is revealed once", async () => {
  const value = { batch_id: "older-batch", jobs: [item("older-child")] };
  const f = fixture({ requestedBatchID: "older-batch", transport: op => op === "batchDetail" ? ok({ kind: "generation_batch", batch: value }) : null });
  f.target.setItem("ai-gateway.generation-history-collapsed", "true");
  f.controller.start(); await tick();
  assert.equal(f.controller.snapshot().groups[0].batch.batch_id, "older-batch");
  assert.equal(f.controller.snapshot().filters.collapsed, false);
  assert.equal(f.snapshots.filter(value => value.targetBatch === "older-batch").length, 1);
  assert.deepEqual(f.calls.find(call => call.operation === "batchDetail").fields, { kind: "generation_batch", id: "older-batch" });
  await f.controller.refresh();
  assert.equal(f.snapshots.filter(value => value.targetBatch).length, 1);
});

test("selected batch failures are recoverable and a confirmed missing batch is explicit", async () => {
  let status = 503;
  const f = fixture({ requestedBatchID: "older-batch", transport: op => op === "batchDetail" ? fail(status) : null });
  f.controller.start(); await tick(); assert.equal(f.notices.length, 0); assert.notEqual(f.controller.snapshot().error, "");
  status = 404; await f.controller.refresh();
  assert.equal(f.notices.length, 1); assert.match(f.notices[0][0], /Пакет/);
});

test("batch hydration rejects wrong identity and late replies after departure", async () => {
  const wait = deferred();
  const f = fixture({ requestedBatchID: "old", transport: op => op === "batchDetail" ? wait.promise : null });
  f.controller.start(); await tick(); f.controller.suspend();
  wait.resolve(ok({ batch: { batch_id: "old", jobs: [item()] } })); await tick();
  assert.equal(f.controller.snapshot().groups.length, 0);
  const wrong = fixture({ requestedBatchID: "old", transport: op => op === "batchDetail" ? ok({ batch: { batch_id: "other", jobs: [] } }) : null });
  wrong.controller.start(); await tick();
  assert.notEqual(wrong.controller.snapshot().error, ""); assert.equal(wrong.notices.length, 0);
});

test("batch hydration revokes access without preserving old media", async () => {
  const f = fixture({ requestedBatchID: "old", transport: op => op === "batchDetail" ? fail(403) : null });
  f.controller.start(); await tick();
  assert.equal(f.revoked, 1); assert.equal(f.controller.snapshot().total, 0);
  const reads = f.calls.length; await f.controller.refresh(); assert.equal(f.calls.length, reads);
});

test("invalid batch links never reach the detail API and empty retained batches remain selectable", async () => {
  const f = fixture({ requestedBatchID: "invalid/batch" }); f.controller.start(); await tick();
  assert.equal(f.calls.some(call => call.operation === "batchDetail"), false); assert.equal(f.notices.length, 1);
  const model = history.projection([], [{ batch_id: "empty", jobs: [] }], {});
  assert.equal(model.groups.length, 1); assert.equal(model.total, 0);
});

test("a known batch survives a partial list error when selected by deep link", async () => {
  const f = fixture({ requestedBatchID: "old", transport: op => op === "batches" ? fail() : op === "batchDetail" ? ok({ batch: { batch_id: "old", jobs: [item()] } }) : null });
  f.controller.start(); await tick();
  assert.equal(f.controller.snapshot().groups[0].batch.batch_id, "old");
  assert.notEqual(f.controller.snapshot().batchError, ""); assert.equal(f.notices.length, 0);
});

test("history projection counts unique jobs, keeps groups and normalizes equivalent failure states", () => {
  const child = item("child", { state: "failed" }), standalone = item("solo", { state: "error" });
  const result = history.projection([child, standalone], [{ batch_id: "batch", jobs: [child] }], { stateFilter: "error" });
  assert.equal(result.total, 2); assert.equal(result.shown, 2);
  assert.deepEqual(result.jobs.map(value => value.job_id), ["solo"]);
  assert.equal(result.groups[0].jobs[0].job_id, "child");
  assert.equal(history.filterJobs([item("wait", { state: "waiting_resources" })], { stateFilter: "waiting_for_resources" }).length, 1);
});

test("history uses the shared live controller and coalesces an SSE refresh burst", async () => {
  const gate = deferred(); let reads = 0;
  const f = fixture({ transport: op => op === "jobs" && ++reads === 1 ? gate.promise : null });
  f.controller.start(); f.controller.refresh(); f.controller.refresh();
  assert.equal(reads, 1);
  gate.resolve(ok({ jobs: [item()], revision: 17 })); await tick();
  assert.equal(reads, 2); assert.equal(f.stream.connects, 1);
  assert.equal(f.polls.length, 2); assert.equal(f.controller.snapshot().loaded, true);
  f.events.onState("live");
  assert.equal(f.controller.snapshot().jobState.live, true);
});

test("pagehide fences late list, variant and detail replies; resume reads without mutations", async () => {
  const jobsWait = deferred(), variantsWait = deferred(); let late = true;
  const f = fixture({ transport: op => late ? op === "jobs" ? jobsWait.promise : op === "variants" ? variantsWait.promise : null : null });
  f.controller.start(); f.controller.suspend(); late = false;
  jobsWait.resolve(ok({ jobs: [item("obsolete")], revision: 99 })); variantsWait.resolve(ok({ variants: [{ id: 99 }] }));
  await tick(); assert.equal(f.slices.job.get().items.length, 0); assert.equal(f.variants.length, 0);
  f.controller.resume(); await tick();
  assert.equal(f.slices.job.get().items[0].job_id, "job-1");
  assert.equal(f.calls.some(call => ["cancel", "winner", "branch"].includes(call.operation)), false);
  assert.equal(f.calls[0].signal.aborted, true);
});

test("hidden history closes its stream and resumes with a fresh read", async () => {
  const f = fixture(); f.controller.start(); await tick();
  f.controller.setVisible(false); assert.ok(f.stream.closes > 0);
  const reads = f.calls.filter(call => call.operation === "jobs").length;
  f.controller.setVisible(true); await tick();
  assert.equal(f.calls.filter(call => call.operation === "jobs").length, reads + 1);
  assert.equal(f.stream.connects, 2);
});

test("list errors retain data and filters while an explicit refresh recovers", async () => {
  let broken = false;
  const f = fixture({ transport: op => op === "jobs" && broken ? fail() : null });
  f.controller.start(); await tick(); f.controller.setFilters({ stateFilter: "completed", templateFilter: "text-to-image" });
  broken = true; await f.controller.refresh();
  assert.equal(f.controller.snapshot().total, 1); assert.match(f.controller.snapshot().error, /Test failure/);
  broken = false; await f.controller.refresh();
  assert.equal(f.controller.snapshot().error, ""); assert.equal(f.controller.snapshot().filters.stateFilter, "completed");
});

test("malformed jobs are not interpreted as an empty successful history", async () => {
  let malformed = false;
  const f = fixture({ transport: op => op === "jobs" && malformed ? ok({ message: "not a list" }) : null });
  f.controller.start(); await tick(); malformed = true; await f.controller.refresh();
  assert.equal(f.controller.snapshot().total, 1); assert.notEqual(f.controller.snapshot().error, "");
});

test("batch read failure retains its last known group with an explicit partial error", async () => {
  let broken = false;
  const f = fixture({ transport: op => op === "batches" && broken ? fail() : null });
  f.batches = [{ batch_id: "batch-1", jobs: [item("child")] }]; f.controller.start(); await tick();
  broken = true; await f.controller.refresh();
  assert.equal(f.controller.snapshot().groups.length, 1); assert.notEqual(f.controller.snapshot().batchError, "");
});

for (const operation of ["jobs", "batches", "variants", "detail"]) test(`${operation} access loss clears data and stops all reads`, async () => {
  let revoked = false;
  const f = fixture({ transport: op => revoked && op === operation ? fail(403) : null });
  f.controller.start(); await tick(); revoked = true;
  if (operation === "variants") await f.controller.refreshVariants();
  else if (operation === "detail") await f.controller.details("job-1");
  else await f.controller.refresh();
  assert.equal(f.controller.snapshot().authRequired, true); assert.equal(f.controller.snapshot().total, 0);
  assert.equal(f.revoked, 1);
  const before = f.calls.length; await f.controller.refresh(); f.controller.resume(); f.controller.setVisible(true);
  assert.equal(f.calls.length, before);
});

test("an old job deep link clears incompatible filters and expands only after authoritative data", async () => {
  const f = fixture({ requestedJobID: "older-job" });
  f.detail = { job: item("older-job"), transitions: [] };
  f.controller.setFilters({ stateFilter: "error", templateFilter: "minimax-h3-video" });
  f.target.setItem("ai-gateway.generation-history-collapsed", "true");
  f.controller.start(); await tick();
  assert.equal(f.controller.snapshot().total, 2);
  assert.equal(f.controller.snapshot().filters.collapsed, false);
  assert.equal(f.controller.snapshot().filters.stateFilter, "");
  assert.equal(f.snapshots.filter(value => value.target === "older-job").length, 1);
  await f.controller.refresh(); assert.equal(f.snapshots.filter(value => value.target).length, 1);
});

test("unknown selected job is not declared gone after a transport failure", async () => {
  let missing = false;
  const f = fixture({ requestedJobID: "old-job", transport: op => op === "detail" ? fail(missing ? 404 : 503) : null });
  f.controller.start(); await tick(); assert.equal(f.notices.length, 0);
  missing = true; await f.controller.refresh(); assert.equal(f.notices.length, 1);
});

test("timeline requests coalesce, retry failures and reject another job identity", async () => {
  const wait = deferred(); let calls = 0;
  const f = fixture({ transport: op => op === "detail" && ++calls === 1 ? wait.promise : null });
  f.controller.start(); await tick(); const a = f.controller.details("job-1"), b = f.controller.details("job-1");
  assert.equal(calls, 1); wait.resolve(fail()); await a; await b;
  assert.equal(f.controller.snapshot().details["job-1"].status, "error");
  await f.controller.details("job-1"); assert.equal(f.controller.snapshot().details["job-1"].status, "ready");
  f.detail = { job: item("foreign"), transitions: [] };
  await f.controller.details("job-1", true); assert.equal(f.controller.snapshot().details["job-1"].status, "error");
});

test("list changes invalidate timeline data without applying a late old transition response", async () => {
  const wait = deferred();
  const f = fixture({ transport: op => op === "detail" ? wait.promise : null });
  f.controller.start(); await tick(); const pending = f.controller.details("job-1");
  f.jobs = [item("job-1", { state: "archiving" })]; await f.controller.refresh();
  wait.resolve(ok({ transitions: [{ state: "old" }] })); await pending;
  assert.equal(f.controller.snapshot().details["job-1"], undefined);
});

test("active job cancellation stays with submission; other cancellation is sent once", async () => {
  const wait = deferred(), active = [];
  const f = fixture({ cancelActive: id => { active.push(id); return id === "active"; }, transport: op => op === "cancel" ? wait.promise : null });
  f.jobs = [item("active", { cancellable: true }), item("other", { cancellable: true })];
  f.controller.start(); await tick(); await f.controller.cancelJob("active");
  assert.equal(f.calls.some(call => call.operation === "cancel"), false);
  const a = f.controller.cancelJob("other"); await f.controller.cancelJob("other");
  assert.equal(f.calls.filter(call => call.operation === "cancel").length, 1);
  wait.resolve(ok({})); await a; assert.deepEqual(active, ["active", "other", "other"]);
});

test("acknowledged mutations fence an older history read even when abort is ignored", async () => {
  const wait = deferred(); let reads = 0;
  const f = fixture({ transport: op => op === "jobs" && ++reads === 2 ? wait.promise : null });
  f.jobs = [item("job-1", { cancellable: true })]; f.controller.start(); await tick();
  const old = f.controller.refresh(); f.jobs = [item("job-1", { state: "cancelled" })];
  await f.controller.cancelJob("job-1");
  wait.resolve(ok({ jobs: [item("obsolete")], revision: 100 })); await old;
  assert.equal(f.controller.snapshot().known.get("job-1").state, "cancelled");
  assert.equal(f.controller.snapshot().known.has("obsolete"), false);
});

test("comparison and winner actions use current batch membership and preserve two-item selection", async () => {
  const f = fixture(); f.batches = [{ batch_id: "b", jobs: [item("a"), item("b"), item("c")] }];
  f.controller.start(); await tick();
  for (const id of ["a", "b", "c"]) f.controller.toggleCompare("b", id);
  assert.deepEqual(f.controller.comparison("b").jobs.map(value => value.job_id), ["b", "c"]);
  assert.equal(await f.controller.selectWinner("b", "foreign"), false);
  await f.controller.selectWinner("b", "b");
  assert.equal(f.calls.filter(call => call.operation === "winner").length, 1);
});

test("branch read never launches work and a changed editor can reject its late parameters", async () => {
  const wait = deferred(); let editor = "original", applied = 0;
  const f = fixture({ transport: op => op === "branch" ? wait.promise : null,
    branchEditor: { capture: () => editor, apply: (_payload, _batch, _job, captured) => { if (captured !== editor) throw new Error("Editor changed"); applied++; } } });
  f.batches = [{ batch_id: "batch", winner_job_id: "winner", jobs: [item("winner")] }]; f.controller.start(); await tick();
  const pending = f.controller.branch("batch", "winner"); editor = "new text"; wait.resolve(ok({ values: {} }));
  assert.equal(await pending, false); assert.equal(applied, 0); assert.match(f.notices[0][1], /Editor changed/);
  assert.deepEqual(f.calls.filter(call => !["jobs", "batches", "variants"].includes(call.operation)).map(call => call.operation), ["branch"]);
});

test("postprocess jobs cannot enter ordinary repeat or batch branching", async () => {
  const f = fixture({ branchEditor: { capture: () => {}, apply: () => assert.fail("Must not apply") } });
  const post = item("post", { template_id: "postprocess-image-v1" });
  f.jobs = [post]; f.batches = [{ batch_id: "batch", winner_job_id: "post", jobs: [post] }];
  f.controller.start(); await tick(); f.controller.repeat("post", {});
  assert.equal(await f.controller.branch("batch", "post"), false); assert.equal(f.repeats.length, 0);
});

test("transport preserves existing routes, current CSRF and bounded credentialed reads", async () => {
  const calls = [], transport = history.createTransport({ csrf: () => "latest", fetcher: async (url, options) => {
    calls.push({ url, options }); return { ok: true, status: 200, json: async () => ({}) };
  } });
  for (const operation of ["jobs", "batches", "variants", "detail", "cancel", "cancelBatch", "winner", "branch"]) await transport(operation, { job_id: "job-1" });
  assert.equal(calls[3].url, "/generate/jobs/detail?job_id=job-1");
  assert.equal(calls[7].url, "/generate/jobs/retry");
  for (const { options } of calls) {
    assert.equal(options.credentials, "same-origin"); assert.equal(options.cache, "no-store"); assert.ok(options.signal);
    if (options.method === "GET") assert.equal(options.body, undefined);
    else assert.equal(options.body.get("csrf"), "latest");
  }
});

test("accepted batch handoff fences an older list without submitting work", async () => {
  const wait = deferred(); let reads = 0;
  const f = fixture({ transport: op => op === "jobs" && ++reads === 2 ? wait.promise : null });
  f.controller.start(); await tick();
  f.controller.setCollapsed(true);
  const pending = f.controller.refresh();
  f.controller.acceptBatch({ batch_id: "accepted-batch", jobs: [item("accepted-child")] });
  wait.resolve(ok({ jobs: [], revision: 99 })); await pending;
  assert.equal(f.controller.snapshot().known.has("accepted-child"), true);
  assert.equal(f.controller.snapshot().filters.collapsed, false);
  assert.equal(f.calls.some(call => !["jobs", "batches", "variants"].includes(call.operation)), false);
});

test("failed action keeps the result and permits only an explicit retry", async () => {
  let failures = true;
  const f = fixture({ transport: op => op === "cancel" && failures ? fail() : null });
  f.jobs = [item("job-1", { cancellable: true })]; f.controller.start(); await tick();
  assert.equal(await f.controller.cancelJob("job-1"), false);
  assert.equal(f.controller.snapshot().known.has("job-1"), true);
  assert.deepEqual(f.controller.snapshot().actions, []);
  await f.controller.refresh();
  assert.equal(f.calls.filter(call => call.operation === "cancel").length, 1);
  failures = false; assert.equal(await f.controller.cancelJob("job-1"), true);
  assert.equal(f.calls.filter(call => call.operation === "cancel").length, 2);
});

test("departure fences a late mutation acknowledgement and resume only reads", async () => {
  const wait = deferred();
  const f = fixture({ transport: op => op === "winner" ? wait.promise : null });
  f.batches = [{ batch_id: "batch", jobs: [item()] }]; f.controller.start(); await tick();
  const pending = f.controller.selectWinner("batch", "job-1"); f.controller.suspend();
  f.controller.resume(); await tick(); wait.resolve(ok({ message: "Old acknowledgement" }));
  assert.equal(await pending, false);
  assert.equal(f.notices.length, 0);
  assert.equal(f.calls.filter(call => call.operation === "winner").length, 1);
});
