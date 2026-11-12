const test = require("node:test");
const assert = require("node:assert/strict");
const video = require("../../static/generation-video.js");
const output = require("../../static/generation-output.js");
const assistant = require("../../static/generation-assistant.js");

test("long video has exact duration and does not affect AIHUB", () => {
  const values = { video_long_enabled: "true", video_long_parts: "6", video_duration_seconds: "15" };
  assert.equal(output.plan({ family: "minimax_h3", values }).duration, 90);
  assert.equal(output.plan({ family: "minimax_h3", values: { ...values, generation_workflow: "aihub-minimax-h3" } }).duration, 15);
});

test("part editor preserves common instructions and audio while fixing timestamps", () => {
  let prompt = "Same adult potter, navy jacket, studio.\noverall_soundscape:\nQuiet ambience.";
  prompt = video.editLongPart(prompt, 2, 1, "Lift the blue cup.");
  prompt = video.editLongPart(prompt, 2, 2, "Place that cup down, continuing the motion.");
  const parts = video.longTimeline(prompt, 2);
  assert.ok(parts.every(part => part.valid));
  assert.deepEqual(parts.map(part => part.start), [0, 15]);
  assert.equal(parts[1].text, "Place that cup down, continuing the motion.");
  assert.match(prompt, /^Same adult potter, navy jacket, studio/);
  assert.match(prompt, /overall_soundscape:\nQuiet ambience\.$/);
  assert.equal(video.longTimeline(prompt.replace("00:15", "00:14"), 2)[1].valid, false);
  assert.equal(video.longTimeline(prompt, 3).filter(part => part.valid).length, 0);
});

test("assistant receives long-video settings instead of a single clip", () => {
  const input = { prompt: "Make a cup", draft: "", enabled: true, mode: "minimax-h3-video", workflow: "minimax-h3-video",
    videoMode: "references", duration: "15", longVideo: { enabled: true, parts: 6 }, images: [], audio: {}, video: {} };
  const body = assistant.buildRequest(input);
  assert.equal(body.get("video_long_enabled"), "true");
  assert.equal(body.get("video_long_parts"), "6");
  assert.equal(body.get("video_duration_seconds"), "15");
  assert.equal(body.get("video_long_transition"), "continuous");
  input.longVideo.transition = "cinematic";
  assert.equal(assistant.buildRequest(input).get("video_long_transition"), "cinematic");
});

test("editor and assistant limits grow together and return to ordinary limits", () => {
  const input = { mode: "minimax-h3-video", workflow: "minimax-h3-video", longVideo: { enabled: true, parts: 2 } };
  assert.equal(assistant.promptCharacterLimit(input), 7200);
  input.longVideo.parts = 6;
  assert.equal(assistant.promptCharacterLimit(input), 13600);
  input.longVideo.parts = 20;
  assert.equal(assistant.promptCharacterLimit(input), 36000);
  input.workflow = "aihub-minimax-h3";
  assert.equal(assistant.promptCharacterLimit(input), 7000);
  input.mode = "image-to-image";
  assert.equal(assistant.promptCharacterLimit(input), 4000);
  input.mode = "minimax-h3-video";
  input.longVideo.enabled = false;
  assert.equal(assistant.promptCharacterLimit(input), 7000);
});
