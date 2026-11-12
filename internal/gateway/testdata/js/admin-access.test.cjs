const test = require("node:test");
const assert = require("node:assert/strict");
const { readSettings, validSettings, summary } = require("../../static/admin-access.js");
const { readAccess, userFlags } = require("../../static/admin-access.js");
function form(extra = {}) {
  const data = new URLSearchParams({ grant_quick_generation: "on", grant_text_to_image: "on", image_generation_daily_unlimited: "on", image_generation_total_unlimited: "on", video_generation_daily_unlimited: "on", video_generation_total_unlimited: "on", max_video_generation_quality: "720", account_type: "permanent", invite_ttl_hours: "24", max_uses: "1", ...extra });
  return readSettings(key => data.get(key) || "");
}
test("Harness-only invitations grant no generation, mining or administrative access", () => {
 const s=form({grant_quick_generation:'',grant_harness:'on',queue_priority:'on',pause_mining_for_quick_generation:'on'});
 assert.equal(validSettings(s),true);assert.equal(s.access.harness,true);
 for(const key of ['quick_generation','text_to_image','image_to_image','video','training','queue_priority','pause_mining'])assert.equal(s.access[key],false);
 assert.equal(summary(s.access).find(([key])=>key==='harness')[2],'Разрешено');
});
test("separate image and video limits and account/link lifetimes", () => {
  const s = form({ grant_video: "on", image_generation_daily_unlimited: "", image_generation_daily_limit: "12", video_generation_daily_unlimited: "", video_generation_daily_limit: "2", account_type: "temporary", temporary_account_hours: "168", invite_ttl_hours: "1" });
  assert.equal(validSettings(s), true); assert.equal(s.access.image_daily_limit, 12); assert.equal(s.access.video_daily_limit, 2); assert.equal(s.link_lifetime_hours, 1); assert.equal(s.account_lifetime_seconds, 604800);
  const rows = new Map(summary(s.access).map(([key, , value]) => [key, value]));
  assert.equal(rows.get("photo"), "12 в сутки; без общего ограничения"); assert.equal(rows.get("video"), "2 в сутки; без общего ограничения"); assert.equal(rows.get("quality"), "720p");
});
test("training-only priority does not grant generation or mining pause", () => {
  const s = form({ grant_quick_generation: "", grant_train_image_lora: "on", queue_priority: "on", pause_mining_for_quick_generation: "on", grant_advanced_generation_settings: "on" });
  assert.equal(validSettings(s), true); assert.equal(s.access.queue_priority, true); assert.equal(s.access.pause_mining, false); assert.equal(s.access.text_to_image, false); assert.equal(s.access.advanced_settings, false);
});
test("invalid and incompatible saved settings never qualify for application", () => {
  for (const mutate of [s => s.version = 2, s => s.access.video = "true", s => s.access.max_video_quality = 2160, s => s.access.image_daily_limit = -1, s => s.max_uses = 0, s => s.account_lifetime_seconds = 3601, s => s.access.text_to_image = false]) { const s = form(); mutate(s); assert.equal(validSettings(s), false); }
});
test("explicit unlimited overrides stale inputs, bounded blanks stay invalid", () => {
  assert.equal(form({ image_generation_daily_limit: "12" }).access.image_daily_limit, 0);
  const invalid = form({ image_generation_daily_unlimited: "", image_generation_daily_limit: "" });
  assert.equal(validSettings(invalid), false); assert.equal(summary(invalid.access)[0][2], "Укажите лимит");
  assert.equal(validSettings(form({ image_generation_daily_unlimited: "", image_generation_daily_limit: "0" })), false);
});

test("user editor shares the access summary without accepting invitation flags", () => {
  const values = new URLSearchParams({ can_use_quick_generation: 'on', can_generate_video: 'on', grant_text_to_image: 'on', can_train_image_lora: 'on', queue_priority: 'on', video_generation_daily_limit: '2', video_generation_total_unlimited: 'on', image_generation_daily_unlimited: 'on', image_generation_total_unlimited: 'on', max_video_generation_quality: '1080' });
  let a = readAccess(key => values.get(key) || '', userFlags);
  assert.equal(a.text_to_image, false); assert.equal(a.video, true); assert.equal(a.video_daily_limit, 2); assert.equal(a.queue_priority, true);
  assert.equal(summary(a)[1][2], '2 в сутки; без общего ограничения');
  values.delete('can_use_quick_generation'); a = readAccess(key => values.get(key) || '', userFlags);
  assert.equal(a.video, false); assert.equal(a.training, true); assert.equal(a.queue_priority, true);
  values.delete('can_train_image_lora'); a = readAccess(key => values.get(key) || '', userFlags);
  assert.equal(a.queue_priority, false); assert.equal(summary(a)[0][2], 'Нет доступа');
});

test("retired raw service flags do not grant usable access or appear in summaries", () => {
  const s = form({ grant_quick_generation: "", grant_comfyui: "on", grant_openwebui: "on" });
  assert.equal(validSettings(s), false);
  assert.equal(Object.hasOwn(s.access, "comfyui"), false);
  assert.equal(Object.hasOwn(s.access, "openwebui"), false);
  assert.equal(summary(s.access).some(([key]) => key === "comfyui" || key === "openwebui"), false);
});
