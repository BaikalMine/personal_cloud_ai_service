const test = require("node:test");
const assert = require("node:assert/strict");
const payload = require("../../static/generation-payload.js");

const control = (name, value = "", extra = {}) => ({
  nodeType: 1, name, value, type: "text", disabled: false, checked: false, dataset: {},
  closest: () => null, ...extra,
});
const fixture = (items = [], options = {}) => {
  let kind = "krea";
  const elements = [...items];
  elements.namedItem = name => {
    const matches = elements.filter(item => item.name === name);
    return matches.length > 1 ? matches : matches[0] || null;
  };
  const form = {
    elements,
    querySelectorAll: selector => selector.includes('type="number"')
      ? elements.filter(item => item.type === "number" || item.localized)
      : elements.filter(item => item.type === "checkbox" && item.name && !item.disabled),
  };
  const nativeEntries = () => elements.filter(item => item.name && !item.disabled && !["button", "submit"].includes(item.type) && (!["checkbox", "radio"].includes(item.type) || item.checked)).map(item => [item.name, item.value]);
  const root = { querySelectorAll: () => [], ...options.root };
  const adapter = payload.createAdapter({ form, root, loraKind: () => kind, formData: nativeEntries });
  return { adapter, form, setKind: value => { kind = value; } };
};
const baseState = () => ({
  selection: { scenarioID: "image-to-image", workflowID: "edit" },
  assistant: { original: "", suggestion: "", action: "" },
  media: { images: [], audio: {}, video: {}, assets: new Map(), pending: [] },
  editor: { step: 2 },
});

test("new model parameter round-trips without another serialization allowlist", () => {
  const numeric = control("future_model_parameter", "1,75", { localized: true });
  const flag = control("future_model_toggle", "on", { type: "checkbox" });
  const unused = control("inactive_model_parameter", "kept", { disabled: true });
  const f = fixture([numeric, flag, unused]);
  const state = baseState();
  const launch = f.adapter.launch(state);
  assert.equal(launch.get("future_model_parameter"), "1.75");
  assert.equal(launch.get("future_model_toggle"), "false");
  assert.equal(launch.has("inactive_model_parameter"), false);
  const draft = f.adapter.draft(state);
  assert.equal(draft.get("future_model_parameter"), "1,75");
  assert.equal(draft.get("inactive_model_parameter"), "kept");
  numeric.value = "0";
  unused.value = "";
  flag.checked = true;
  f.adapter.apply(Object.fromEntries(draft));
  assert.equal(numeric.value, "1,75");
  assert.equal(unused.value, "kept");
  assert.equal(flag.checked, false);
  assert.equal(f.adapter.launch(state).get("future_model_parameter"), "1.75");
});

test("radio and checkbox values preserve successful-control and draft semantics", () => {
  const choices = [control("mode", "first", { type: "radio" }), control("mode", "second", { type: "radio", checked: true })];
  const hidden = control("hidden_toggle", "on", { type: "checkbox", disabled: true, checked: true });
  const f = fixture([...choices, hidden]);
  assert.equal(f.adapter.launch().get("mode"), "second");
  assert.equal(f.adapter.launch().has("hidden_toggle"), false);
  assert.equal(f.adapter.draft().get("mode"), "second");
  assert.equal(f.adapter.draft().get("hidden_toggle"), "true");
  f.adapter.set("mode", "missing");
  assert.equal(choices[1].checked, true);
  f.adapter.set("hidden_toggle", "1");
  assert.equal(hidden.checked, true);
  f.adapter.set("hidden_toggle", "false");
  assert.equal(hidden.checked, false);
});

test("duplicated LoRA fields follow active family even when temporarily disabled", () => {
  const lora = kind => control("lora_1", `${kind}-model`, {
    disabled: kind !== "krea", closest: () => ({ dataset: { loraSlots: kind } }),
  });
  const krea = lora("krea"), flux = lora("flux");
  const f = fixture([krea, flux]);
  f.setKind("flux");
  assert.deepEqual(f.adapter.controls("lora_1"), [flux]);
  assert.equal(f.adapter.draft().get("lora_1"), "flux-model");
  f.adapter.set("lora_1", "restored-flux");
  assert.equal(flux.value, "restored-flux");
  assert.equal(krea.value, "krea-model");
});

test("LoRA selections restore numerically before strengths and other model settings", () => {
  const f = fixture([control("lora_1"), control("lora_2"), control("lora_10"), control("lora_model_strength_2", "0.5")]);
  const observed = [];
  f.adapter.apply({ lora_10: "ten", lora_model_strength_2: "0.8", lora_2: "two", lora_1: "one" }, {
    afterLora: () => observed.push(f.form.elements.map(item => item.value)),
  });
  assert.deepEqual(observed, [["one", "", "", "0.5"], ["one", "two", "", "0.5"], ["one", "two", "ten", "0.5"]]);
  assert.equal(f.adapter.controls("lora_model_strength_2")[0].value, "0.8");
});

test("file objects never enter parameter bodies and are never restored as scalar controls", () => {
  const file = control("source_upload", { name: "private.jpg" }, { type: "file" });
  const image = control("input_image", "current");
  const f = fixture([file, image]);
  assert.equal(f.adapter.launch().has("source_upload"), false);
  assert.equal(f.adapter.draft().has("source_upload"), false);
  f.adapter.apply({ source_upload: "not-a-file", input_image: "unverified" });
  assert.deepEqual(file.value, { name: "private.jpg" });
  assert.equal(image.value, "current");
});

test("launch isolates inactive media and deletes inherited reference metadata", () => {
  const f = fixture([control("image_role_4", "style"), control("image_source_id_4", "stale"), control("input_image_4", "old")]);
  const state = baseState();
  state.media.images = [
    { slot: 1, value: "first.png", active: true, role: "style", name: "first", reference: { role: "first_frame", source: "gallery", sourceID: "42", sourceName: "first" } },
    { slot: 4, value: "fourth.png", active: false, role: "identity", name: "fourth", reference: { role: "identity", source: "device" } },
  ];
  state.media.audio = { value: "voice.wav", active: false, name: "voice" };
  state.media.video = { value: "motion.mp4", active: false, name: "motion", referenceAudio: true };
  const launch = f.adapter.launch(state);
  assert.equal(launch.get("input_image"), "first.png");
  assert.equal(launch.get("image_role_1"), "first_frame");
  assert.equal(launch.get("image_source_id_1"), "42");
  assert.equal(launch.get("input_image_4"), "");
  assert.equal(launch.has("image_role_4"), false);
  assert.equal(launch.has("image_source_id_4"), false);
  assert.equal(launch.get("input_audio"), "");
  assert.equal(launch.get("input_video"), "");
  assert.equal(launch.get("video_reference_audio"), "false");
  const draft = f.adapter.draft(state);
  assert.equal(draft.get("input_image_4"), "fourth.png");
  assert.equal(draft.get("image_role_1"), "style");
  assert.equal(draft.get("image_role_4"), "identity");
  assert.equal(draft.get("draft_name_input_image_4"), "fourth");
  assert.equal(draft.get("input_audio"), "voice.wav");
  assert.equal(draft.get("input_video"), "motion.mp4");
});

test("active video audio flag requires an actual uploaded video", () => {
  const f = fixture();
  const state = baseState();
  state.media.video = { active: true, referenceAudio: true };
  assert.equal(f.adapter.launch(state).get("video_reference_audio"), "false");
  state.media.video.value = "video.mp4";
  assert.equal(f.adapter.launch(state).get("video_reference_audio"), "true");
});

test("assistant provenance, human decisions and editable draft state are separate", () => {
  const f = fixture();
  const state = baseState();
  state.assistant = {
    enabled: true, original: "original", suggestion: "suggestion", action: "edited_after_apply",
    correlationID: "correlation", template: "template", think: true, jobID: "job", draft: "edited",
    references: [{ role: "style" }], stale: true, draftEdited: true,
    corrections: { first: { text: "human correction", source: "human" } },
  };
  state.editor = { parentJobID: "parent", quality: "quality", step: 3, advanced: true };
  const launch = f.adapter.launch(state);
  assert.equal(launch.get("assistant_requested"), "true");
  assert.equal(launch.get("assistant_applied"), "true");
  assert.equal(launch.get("assistant_action"), "edited_after_apply");
  assert.equal(launch.get("assistant_original_prompt"), "original");
  assert.equal(launch.get("assistant_suggestion"), "suggestion");
  assert.equal(launch.get("assistant_template_used"), "template");
  assert.equal(launch.get("assistant_think_used"), "true");
  assert.equal(launch.get("correlation_id"), "correlation");
  assert.equal(launch.get("parent_job_id"), "parent");
  assert.equal(launch.has("assistant_draft"), false);
  const draft = f.adapter.draft(state);
  assert.equal(draft.get("assistant_draft"), "edited");
  assert.equal(draft.get("assistant_job_id"), "job");
  assert.equal(draft.get("assistant_stale"), "true");
  assert.deepEqual(JSON.parse(draft.get("assistant_corrections")), state.assistant.corrections);
  assert.deepEqual(JSON.parse(draft.get("assistant_references")), state.assistant.references);
  state.assistant.action = "kept_original";
  assert.equal(f.adapter.launch(state).get("assistant_applied"), "false");
});

test("draft asset bindings distinguish available, missing and newly selected media", () => {
  const f = fixture();
  const state = baseState();
  state.media.assets = new Map([
    ["input_image", { id: "old-id", name: "old.jpg", available: true }],
    ["input_audio", { name: "missing.wav", available: false }],
    ["input_video", { id: "expired-id", name: "expired.mp4", available: false }],
  ]);
  state.media.pending = [{ field: "input_image", source: { name: "new.jpg" } }];
  const draft = f.adapter.draft(state);
  assert.equal(draft.has("draft_asset_input_image"), false);
  assert.equal(draft.get("draft_pending_input_image"), "new.jpg");
  assert.equal(draft.get("draft_pending_input_audio"), "missing.wav");
  assert.equal(draft.get("draft_asset_input_video"), "expired-id");
  assert.equal(draft.get("draft_name_input_video"), "expired.mp4");
});

test("selection validation is read-only and rejects unavailable or incompatible references", () => {
  const scenario = { dataset: { workflowId: "image-to-image" } };
  const workflow = { dataset: { presetId: "edit", templateId: "image-to-image", family: "flux2" } };
  const model = control("model", "current", { options: [
    { value: "flux", dataset: { family: "flux2", available: "true" } },
    { value: "krea", dataset: { family: "krea2", available: "true" } },
    { value: "offline", dataset: { family: "flux2", available: "false" } },
  ] });
  const f = fixture([model], { root: { querySelectorAll: selector => selector === ".scenario-choice" ? [scenario] : selector === ".generation-workflow-choice" ? [workflow] : [{ value: "lora" }] } });
  const valid = { template_id: "image-to-image", generation_workflow: "edit", model: "flux", lora_1: "lora" };
  assert.deepEqual(f.adapter.resolveSelection(valid), { scenario, workflow });
  for (const invalid of [{ template_id: "missing" }, { generation_workflow: "missing" }, { model: "krea" }, { model: "offline" }, { lora_1: "missing" }]) {
    assert.throws(() => f.adapter.resolveSelection({ ...valid, ...invalid }), /недоступ/);
    assert.equal(model.value, "current");
  }
  workflow.dataset.templateId = "text-to-image";
  assert.throws(() => f.adapter.resolveSelection(valid), /не соответствует/);
  workflow.dataset.templateId = "image-to-image";
  scenario.disabled = true;
  assert.throws(() => f.adapter.resolveSelection(valid), /недоступ/);
  for (const invalid of [null, [], "text"]) assert.throws(() => f.adapter.resolveSelection(invalid), /Некорректные/);
});
