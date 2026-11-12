const test = require("node:test");
const assert = require("node:assert/strict");
const model = require("../../static/task-model.js");

test("task selection is typed and bounded", () => {
  assert.deepEqual(model.selection("generation:job-1"), { kind: "generation", id: "job-1" });
  for (const invalid of ["", "admin:job", "generation:", "generation:a:b", "generation:../a", `generation:${"a".repeat(201)}`]) assert.equal(model.selection(invalid), null);
});
test("pages deduplicate by source identity and retain creation ordering", () => {
  const task = (kind, id, date, state = "queued") => ({ kind, id, created_at: date, state });
  const a = task("generation", "a", "2026-09-01T10:00:00Z");
  const b = task("lora_training", "a", a.created_at);
  const result = model.merge([[a, b], [{ ...a, state: "completed" }]]);
  assert.deepEqual(result.map(model.key), ["lora_training:a", "generation:a"]);
  assert.equal(result[1].state, "completed");
});
test("cancellation never replaces a confirmed terminal state", () => {
  assert.equal(model.status({ state: "running", cancellation_pending: true }).label, model.labels.cancelling);
  for (const state of ["completed", "failed", "cancelled", "expired"]) assert.equal(model.status({ state, cancellation_pending: true }).label, model.labels[state]);
  assert.equal(model.status({ state: "unknown" }).tone, "info");
});
test("only actual generation parents form groups", () => {
  const result = model.grouped([{ kind: "generation", id: "a", batch_id: "batch" }, { kind: "lora_caption", id: "b", dataset_id: "dataset" }, { kind: "generation", id: "c", batch_id: "batch" }]);
  assert.equal(result.length, 2);
  assert.deepEqual(result[0].batch.children.map(task => task.id), ["a", "c"]);
  assert.equal(result[1].task.id, "b");
});
test("date ranges use local midnight and inclusive end day", () => {
  const query = model.apiQuery({ from: "2026-09-01", to: "2026-09-01", search: "%_" }, "opaque");
  assert.equal(new Date(query.get("from")).getHours(), 0);
  assert.equal(new Date(query.get("before")).getDate(), 2);
  assert.equal(query.get("search"), "%_");
  assert.equal(query.get("cursor"), "opaque");
  for (const date of ["2026-02-30", "2026-13-01", "0000-01-01", "bad"]) assert.throws(() => model.dateBoundary(date));
  assert.throws(() => model.apiQuery({ from: "2026-09-02", to: "2026-09-01" }));
});

test("row actions translate only known server hints and keep pending cancellation inspectable", () => {
  for (const [kind, code, icon] of [["generation", "cancel", "square"], ["generation", "edit_generation", "sliders-horizontal"], ["lora_caption", "open_caption", "image"], ["lora_training", "open_training", "folder-open"], ["lora_training", "download_training", "download"]]) {
    const action = model.rowAction({ kind, state: "completed", next_action: code });
    assert.equal(action.code, code); assert.equal(action.icon, icon); assert.ok(action.label.length);
  }
  assert.equal(model.rowAction({ kind: "generation", next_action: "download_training" }).code, "inspect");
  assert.equal(model.rowAction({ kind: "generation", next_action: "unknown" }).code, "inspect");
  assert.equal(model.rowAction({ kind: "generation", next_action: "cancel", cancellation_pending: true }).code, "inspect");
  assert.equal(model.rowAction({ kind: "generation", state: "failed", next_action: "edit_generation" }).label, "Изменить и повторить");
  assert.equal(model.rowAction({ kind: "generation", state: "completed", next_action: "edit_generation" }).label, "Использовать параметры");
});

test("series summaries report retained counts without inventing progress", () => {
  const task = { kind: "lora_caption_series", series: { total: 4, completed: 1, failed: 1, cancelled: 1, unavailable: 1 } };
  assert.equal(model.seriesSummary(task), "Готово 1 из 4 · Ошибки: 1 · Отменено: 1 · Недоступно: 1");
  assert.equal(model.seriesSummary({ ...task, kind: "lora_caption" }), "");
  for (const values of [{ completed: 5 }, { failed: -1 }, { total: "4" }, { unavailable: null }, { total: Infinity }]) {
    assert.equal(model.seriesSummary({ ...task, series: { ...task.series, ...values } }), "");
  }
  assert.equal(model.seriesSummary({ ...task, series: { total: 3, completed: 3, failed: 0, cancelled: 0, unavailable: 0 } }), "Готово 3 из 3");
});

test("series filters and selection retain distinct identities across pages", () => {
  const query = model.apiQuery({ kind: "lora_caption_series", state: "partial", search: "original" }, "cursor");
  assert.equal(query.get("kind"), "lora_caption_series");
  assert.equal(query.get("state"), "partial");
  const item = { kind: "lora_caption_series", id: "series-1", state: "partial", created_at: "2026-09-01T10:00:00Z" };
  assert.deepEqual(model.selection(model.key(item)), { kind: "lora_caption_series", id: "series-1" });
  assert.equal(model.merge([[item], [item]]).length, 1);
  assert.equal(model.status(item).tone, "warning");
  assert.equal(model.rowAction(item).code, "inspect");
});
