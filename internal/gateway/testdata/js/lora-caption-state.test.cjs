const test = require("node:test");
const assert = require("node:assert/strict");
const { matches, latest, reconcile, createPoller } = require("../../static/lora-caption-state.js");
const copy = (value) => JSON.parse(JSON.stringify(value));
const fixture = () => {
  const manifest = { settings: { trigger_word: "person_x", concept_type: "character" }, images: [{ id: "frame", asset_id: "asset", caption: "", caption_revision: "first" }] };
  const job = { job_id: "job", dataset_id: "set", image_id: "frame", created_at: "2026-09-05T12:00:00Z", state: "completed", caption: "person_x, light hair", source: { image: copy(manifest.images[0]), trigger_word: "person_x", concept_type: "character" } };
  return { manifest, job };
};
test("durable result applies once, recording its job and a new caption revision", () => {
  const { manifest, job } = fixture();
  assert.equal(reconcile(manifest, [job], () => "second"), 1);
  assert.equal(manifest.images[0].caption, job.caption);
  assert.equal(manifest.images[0].caption_job_id, "job");
  assert.equal(manifest.images[0].caption_revision, "second");
  assert.equal(reconcile(copy(manifest), [job]), 0);
});
test("late captions preserve manual edits, including edit-and-revert, trigger and asset changes", () => {
  for (const change of [m => m.images[0].caption = "manual", m => m.images[0].caption_revision = "typed-and-reverted", m => m.images[0].asset_id = "other", m => m.settings.trigger_word = "other", m => m.settings.concept_type = "style", m => m.images[0].excluded = true, m => m.images = []]) {
    const { manifest, job } = fixture(); change(manifest); const before = copy(manifest);
    assert.equal(reconcile(manifest, [job]), 0); assert.deepEqual(manifest, before);
  }
});
test("only the newest job for each frame may apply, even when an older one matches", () => {
  const { manifest, job } = fixture(); const newer = { ...job, job_id: "new", created_at: "2026-09-05T13:00:00Z", state: "cancelled" };
  assert.equal(latest([newer, job]).get("frame"), newer);
  assert.equal(reconcile(manifest, [newer, job]), 0);
  assert.equal(matches(manifest, manifest.images[0], job), true);
});
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; };
const drain = () => new Promise(resolve => setImmediate(resolve));
function polling(request) {
  let sequence = 0; const timers = new Map();
  const poller = createPoller({ request, schedule: (work) => { timers.set(++sequence, work); return sequence; }, unschedule: (id) => timers.delete(id) });
  return { poller, tick: async () => { const entry = timers.entries().next().value; if (entry) { timers.delete(entry[0]); entry[1](); await drain(); } }, timers };
}
test("caption poller restores jobs without another POST and ignores late responses from other sets", async () => {
  const old = deferred(); const { job } = fixture();
  const f = polling(async id => id === "old" ? old.promise : { jobs: [job, { ...job, dataset_id: "foreign" }] });
  f.poller.select("old"); f.poller.select("set"); await drain();
  assert.deepEqual(f.poller.state.jobs, [job]);
  old.resolve({ jobs: [{ ...job, dataset_id: "old" }] }); await drain();
  assert.deepEqual(f.poller.state.jobs, [job]); f.poller.dispose(); assert.equal(f.timers.size, 0);
});
test("504 keeps received jobs, counts down, and reconnects without duplicating requests", async () => {
  const { job } = fixture(); let offline = false; let requests = 0;
  const f = polling(async () => { requests++; if (offline) throw new Error("HTTP 504"); return { jobs: [job] }; });
  f.poller.select("set"); await drain(); offline = true; await f.poller.refresh();
  assert.deepEqual(f.poller.state.jobs, [job]); assert.equal(f.poller.state.retrySeconds, 3);
  await f.tick(); assert.equal(f.poller.state.retrySeconds, 2); await f.tick(); assert.equal(f.poller.state.retrySeconds, 1);
  offline = false; await f.tick(); assert.equal(requests, 3); assert.equal(f.poller.state.error, ""); f.poller.dispose();
});
test("page disposal cancels only polling and never modifies durable jobs", async () => {
  const held = deferred(); let signal; const f = polling(async (_, abort) => { signal = abort; return held.promise; });
  f.poller.select("set"); f.poller.dispose(); assert.equal(signal.aborted, true);
  held.resolve({ jobs: [fixture().job] }); await drain(); assert.deepEqual(f.poller.state.jobs, []); assert.equal(f.timers.size, 0);
});
test("returning from the browser back-forward cache restarts polling", async () => {
  let requests = 0; const { job } = fixture();
  const f = polling(async () => { requests++; return { jobs: [job] }; });
  f.poller.select("set"); await drain(); f.poller.dispose(); f.poller.resume(); await drain();
  assert.equal(requests, 2); assert.deepEqual(f.poller.state.jobs, [job]); f.poller.dispose();
});
