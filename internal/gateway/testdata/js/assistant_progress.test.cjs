const test = require("node:test");
const assert = require("node:assert/strict");
const {metrics} = require("../../static/generation-assistant-progress.js");
test("live text uses elapsed time without invented token usage", () => {
 const view = metrics({state:"running",progress:{phase:"writing",prompt:"A vase",started_at:"2026-10-01T10:00:00Z",usage:{prompt_tokens:0,completion_tokens:0}}}, Date.parse("2026-10-01T10:00:12Z"));
 assert.equal(view.prompt,"A vase"); assert.equal(view.time,"Время 0:12");
 assert.equal(view.speed,""); assert.match(view.tokens,/ожидаем/);
});
test("completed usage and duration are factual and stable", () => {
 const job={state:"completed",progress:{started_at:"2026-10-01T10:00:00Z",finished_at:"2026-10-01T10:00:20Z"},result:{usage:{prompt_tokens:34,completion_tokens:12,completion_duration_ms:2000}}};
 const view=metrics(job,Date.parse("2026-10-01T11:00:00Z"));
 assert.equal(view.time,"Время 0:20");assert.equal(view.prompt,"");assert.equal(view.speed,"6 ток/с");
 assert.match(view.tokens,/34 вход/);assert.match(view.tokens,/12 выход/);
});
