const test = require("node:test");
const assert = require("node:assert/strict");
const view = require("../../static/generation-results.js");
const lightbox = require("../../static/generation-lightbox.js");

test("recent runs bound the studio to eight chronologically ordered launches", () => {
  const jobs = Array.from({ length: 30 }, (_, i) => ({ job_id: `job-${i}`, created_at: new Date(1000 * i).toISOString() }));
  const group = { batch: { batch_id: "batch-1", created_at: new Date(40000).toISOString() }, jobs: [{ job_id: "child" }] };
  const state = { jobs, groups: [group] }, before = JSON.stringify(state), result = view.recentRuns(state);
  assert.equal(result.all.length, 31); assert.equal(result.items.length, 8);
  assert.deepEqual(result.items.slice(0, 3).map(run => run.key), ["batch:batch-1", "job:job-29", "job:job-28"]);
  assert.equal(JSON.stringify(state), before);
});

test("selected older runs stay reachable without growing the recent strip", () => {
  const jobs = Array.from({ length: 12 }, (_, i) => ({ job_id: `job-${i}`, created_at: new Date(1000 * i).toISOString() }));
  const result = view.recentRuns({ jobs }, "job:job-0");
  assert.equal(result.items.length, 8); assert.equal(result.items[7], result.selected);
  assert.equal(result.selected.job.job_id, "job-0");
  assert.equal(view.recentRuns({ jobs }, "job:missing").selected, null);
  assert.equal(view.recentRuns({}).items.length, 0);
});

test("a batch cover prefers its winner then a non-sensitive retained preview", () => {
  const hidden = { job_id: "hidden", preview: { sensitive: true } }, safe = { job_id: "safe", preview: { sensitive: false } };
  const run = { group: { batch: {}, jobs: [hidden, safe] } };
  assert.equal(view.runCover(run), safe);
  run.group.batch.winner_job_id = "hidden"; assert.equal(view.runCover(run), hidden);
  assert.equal(view.runCover({ group: { batch: {}, jobs: [] } }), undefined);
});

test("result expiry distinguishes unknown, elapsed and retained media", () => {
  assert.equal(view.formatExpiry(NaN), "Срок хранения неизвестен");
  assert.equal(view.formatExpiry(-1), "Удаляется при следующей очистке");
  assert.equal(view.formatExpiry(1), "Удалится через 1 мин.");
  assert.equal(view.formatExpiry(3660000), "Удалится через 1 ч. 1 мин.");
  assert.equal(view.formatExpiry(90000000), "Удалится через 1 дн. 1 ч.");
});

test("result labels preserve failure and resource aliases and readable model names", () => {
  assert.equal(view.stateLabel("failed"), view.stateLabel("error"));
  assert.equal(view.stateLabel("waiting_resources"), view.stateLabel("waiting_for_resources"));
  assert.equal(view.cleanModelName("folder\\model.safetensors"), "model");
  assert.equal(view.formatTime("invalid"), "");
  assert.equal(view.countLabel(1), "1 задание");
  assert.equal(view.countLabel(12), "12 заданий");
  assert.equal(view.countLabel(22), "22 задания");
});

test("suspended preview rejects late hover and focus playback until resumed", () => {
  const events = {}, video = { addEventListener() {}, play() { this.plays++; return Promise.resolve(); }, pause() {}, plays: 0 };
  const button = { querySelector: () => video, addEventListener: (name, action) => events[name] = action };
  let active = true;
  const controller = lightbox.createController({ documentRef: {} });
  controller.wireVideoPreview(button, {}, { canPlay: () => active });
  events.pointerenter(); assert.equal(video.plays, 1);
  active = false; events.pointerenter(); events.focusin(); assert.equal(video.plays, 1);
  active = true; events.focusin(); assert.equal(video.plays, 2);
});
