const test = require("node:test");
const assert = require("node:assert/strict");
const assistant = require("../../static/generation-assistant.js");
const jobs = require("../../static/generation-assistant-jobs.js");
const tick = () => new Promise(resolve => setImmediate(resolve));
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; };
const id = "assistant-session-request-0001";
const proposal = (text = "Prepared proposal", correlationID = id) => ({ prompt: text, correlation_id: correlationID, references: [], model: "synthetic" });
const ok = payload => ({ ok: true, status: 200, payload });
const fail = message => ({ ok: false, status: 503, payload: { error: message } });
const baseInput = () => ({ prompt: "Original text", draft: "", negative: "", negativeDraft: "", enabled: true, workflow: "photoflow-krea2", model: "krea2.safetensors",
  mode: "text-to-image", videoMode: "frames", duration: "5", profile: "workflow-default", think: false,
  images: [], audio: { local: "", value: "" }, video: { local: "", value: "" } });

test("AIHUB includes every selected picture and active media context", () => {
  const input = {...baseInput(), workflow:"aihub-minimax-h3", mode:"minimax-h3-video", videoMode:"references",
    aiHub:{mode:"video",audioSource:"ltx",audioPrompt:"Exact words",voiceConversion:true,videoStart:"2"},
    images:Array.from({length:9},(_,i)=>({number:i+1,role:"identity",value:`u/${i}.png`})),
    audio:{value:"u/voice.wav"},video:{value:"u/motion.mp4"}};
  const body=assistant.buildRequest(input);
  assert.equal(body.get("input_image_9"),"u/8.png");
  assert.equal(body.get("image_role_9"),"identity");
  assert.equal(body.get("input_video"),"u/motion.mp4");
  assert.equal(body.get("input_audio"),"u/voice.wav");
  assert.equal(body.get("video_has_audio"),"false");
  assert.equal(body.get("aihub_audio_prompt"),"Exact words");
  assert.equal(body.get("video_reference_start"),"2");
});

test("AIHUB audio or source context changes fence an old assistant proposal", async () => {
  const f=fixture({input:{workflow:"aihub-minimax-h3",mode:"minimax-h3-video",videoMode:"references",aiHub:{mode:"photo",audioSource:"ltx",audioPrompt:"Hello"}}});
  await f.session.improve();
  assert.equal(f.session.snapshot().canApply,true);
  f.input.aiHub={...f.input.aiHub,audioPrompt:"Different words"};
  f.session.syncContext();
  assert.equal(f.session.snapshot().canApply,false);
  assert.equal(await f.session.apply(),false);
});

test("changing long-video transition fences a proposal planned for another camera style", async () => {
  const f = fixture({ input: { workflow: "minimax-h3-video", mode: "minimax-h3-video", videoMode: "references",
    longVideo: { enabled: true, parts: 3, transition: "continuous" } } });
  await f.session.improve();
  assert.equal(f.session.snapshot().canApply, true);
  f.input.longVideo = { ...f.input.longVideo, transition: "cinematic" };
  f.session.syncContext();
  assert.equal(f.session.snapshot().canApply, false);
  assert.equal(await f.session.apply(), false);
});

function fixture(options = {}) {
  const input = { ...baseInput(), ...options.input }, calls = [], views = [], saved = [], applied = [], restores = [];
  const storage = options.storage || new Map();
  let state = assistant.createState(), time = 100000, timerID = 0, dirty = 0;
  const timers = new Map();
  const target = { getItem: key => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value), removeItem: key => storage.delete(key) };
  const persistence = options.persistence || assistant.createPersistence({ storage: () => target, owner: "1", validID: jobs.validID, onChange: () => dirty++ });
  const session = assistant.createSession({
    state: { get: () => state, dispatch: action => { state = assistant.reduce(state, action); } },
    read: () => input, prepare: options.prepare || (async () => {}), durable: options.defaultProtocol ? undefined : options.durable === undefined ? false : options.durable,
    transport: async (op, body, signal) => {
      calls.push({ op, body: new URLSearchParams(body), signal });
      if (options.transport) return options.transport(op, body, signal);
      return ok(op === "decision" ? { decision: body.get("decision") } : proposal());
    },
    persistence, newRequestID: () => id,
    jobs: { ...jobs, createController: settings => jobs.createController({ ...settings, now: () => time,
      schedule: (fn, delay) => { const key = ++timerID; timers.set(key, { fn, at: time + delay }); return key; }, unschedule: key => timers.delete(key) }) },
    onState: value => views.push(value), onDirty: () => dirty++, onRequest: () => {},
    onProposal: value => { input.draft = value.prompt; input.negativeDraft = value.negative_prompt || ""; }, onSavedResponse: value => saved.push(value),
    onApplied: (value, audio, negative) => { input.prompt = value; input.negative = negative; if (audio) input.aiHub = {...input.aiHub, audioPrompt:audio}; applied.push(value); }, onReset: () => { input.draft = ""; },
    onOriginalRestored: (value, negative, audio) => { input.prompt = value; input.negative = negative; if (input.aiHub) input.aiHub = {...input.aiHub, audioPrompt:audio}; },
    onRestore: (value, draft) => { input.draft = draft; restores.push(value); },
  });
  if (options.initialize !== false) session.initialize();
  return { session, input, calls, views, saved, applied, restores, storage, target, state: () => state, dirty: () => dirty,
    async advance(amount) {
      const end = time + amount;
      for (;;) {
        const next = [...timers].sort((a, b) => a[1].at - b[1].at)[0];
        if (!next || next[1].at > end) break;
        time = next[1].at; timers.delete(next[0]); next[1].fn(); await tick();
      }
      time = end; await tick();
    },
  };
}

for (const profile of ["workflow-default", "photographic", "anime"]) test(`Ultra negative proposal preserves style and restores both fields (${profile})`, async () => {
  const f = fixture({ input: { workflow: "krea2-ultra", profile, cfg: "2", negative: "Original exclusions" },
    transport: async (op, body) => ok(op === "decision" ? { decision: body.get("decision") } : { ...proposal(), negative_prompt: "Blurry, duplicate objects" }) });
  await f.session.improve();
  const request = f.calls.find(call => call.op !== "decision").body;
  assert.equal(request.get("generation_workflow"), "krea2-ultra");
  assert.equal(request.get("assistant_template"), profile);
  assert.equal(request.get("cfg"), "2");
  assert.equal(request.get("negative_prompt"), "Original exclusions");
  assert.equal(f.state().negativeSuggestion, "Blurry, duplicate objects");
  assert.equal(await f.session.apply(), true);
  assert.equal(f.input.negative, "Blurry, duplicate objects");
  assert.equal(await f.session.restoreOriginal(), true);
  assert.equal(f.input.negative, "Original exclusions");
  assert.equal(f.input.prompt, "Original text");
});

test("Ultra at CFG 1 ignores ineffective negative proposals", async () => {
  const f = fixture({ input: { workflow: "krea2-ultra", cfg: "1", negative: "Ignored exclusions" },
    transport: async (op, body) => ok(op === "decision" ? { decision: body.get("decision") } : { ...proposal(), negative_prompt: "Unused exclusions" }) });
  assert.equal(assistant.buildRequest(f.input).get("negative_prompt"), "");
  await f.session.improve();
  assert.equal(f.state().negativeSuggestion, "");
  assert.equal(await f.session.apply(), true);
  assert.equal(f.input.negative, "");
});

for (const workflow of ["moss-image", "krea2-ultra", "krea2-pro", "qwen-image21"]) {
  for (const durable of [false, true]) test(`${workflow} preserves negative-only proposals through ${durable ? "durable" : "direct"} apply and restore`, async () => {
    const f = fixture({ durable, input: { workflow, cfg: "3", negative: "Original exclusions" }, transport: async (op, body) => {
      if (op === "decision") return ok({decision:body.get("decision")});
      const result={...proposal("Original text"),negative_prompt:"watermark, blurry lettering"};
      return ok(durable ? {id,state:"completed",context:body.get("context"),original:body.get("prompt"),original_negative:body.get("negative_prompt"),result} : result);
    } });
    await f.session.improve();
    assert.equal(f.calls[0].body.get("generation_workflow"),workflow);
    assert.equal(f.calls[0].body.get("negative_prompt"),"Original exclusions");
    assert.equal(f.input.negativeDraft,"watermark, blurry lettering");
    f.input.negativeDraft="watermark, muddy texture";
    f.session.editDraft();
    assert.equal(await f.session.apply(),true);
    assert.equal(f.input.prompt,"Original text");
    assert.equal(f.input.negative,"watermark, muddy texture");
    assert.equal(f.session.snapshot().canRestoreOriginal,true);
    assert.equal(await f.session.beforeLaunch(),true);
    assert.equal(f.calls.at(-1).body.get("final_negative_prompt"),"watermark, muddy texture");
    assert.equal(await f.session.restoreOriginal(),true);
    assert.equal(f.input.negative,"Original exclusions");
  });
}

test("negative prompt capability does not leak into PhotoFlow, Flux or video modes",()=>{
  for(const input of [{workflow:"photoflow-krea2"},{workflow:"photoflow-krea2-edit",mode:"image-to-image"},{workflow:"photoflow-flux2-edit",mode:"image-to-image"},{workflow:"moss-image",mode:"minimax-h3-video"}]) {
    assert.equal(assistant.supportsNegative({...baseInput(),...input}),false);
    assert.equal(assistant.buildRequest({...baseInput(),...input}).has("cfg"),false);
  }
});

test("LTX proposal applies both fields without invalidating its own context", async () => {
  const f = fixture({input:{workflow:"aihub-minimax-h3", mode:"minimax-h3-video", aiHub:{mode:"photo",audioSource:"ltx",audioPrompt:"Hello"}},
    transport: async (op, body) => ok(op === "decision" ? {decision:body.get("decision")} : {...proposal(), audio_prompt:'A woman says: "Hello". Quiet room ambience.'})});
  await f.session.improve();
  assert.equal(f.input.aiHub.audioPrompt, "Hello");
  assert.equal(await f.session.apply(), true);
  assert.equal(f.input.aiHub.audioPrompt, f.state().audioSuggestion);
  f.session.syncContext();
  assert.equal(f.state().stale, false);
  assert.equal(await f.session.beforeLaunch(), true);
});

test("soundtrack proposal survives draft restoration and stays out of native mode", async () => {
  const f = fixture();
  f.session.restore({assistant_original_prompt:"Original",assistant_suggestion:"Visual",assistant_audio_suggestion:"Sound",correlation_id:id});
  assert.equal(f.state().audioSuggestion,"Sound");
  const native = fixture({transport: async () => ok({...proposal(),audio_prompt:"Unexpected"})});
  await native.session.improve();
  assert.equal(native.state().audioSuggestion, "");
});

for (const originalAudio of ["Hello", ""]) test(`LTX original survives apply, Keep and draft recovery (${originalAudio || "empty"})`, async () => {
  const f = fixture({input:{workflow:"aihub-minimax-h3", mode:"minimax-h3-video", aiHub:{mode:"photo",audioSource:"ltx",audioPrompt:originalAudio}},
    transport: async (op, body) => ok(op === "decision" ? {decision:body.get("decision")} : {...proposal(), audio_prompt:"Improved soundtrack"})});
  await f.session.improve();
  assert.equal(f.state().originalAudio, originalAudio);
  assert.equal(await f.session.apply(), true);
  assert.equal(await f.session.keep(), true);
  assert.equal(f.state().action, "applied");
  assert.equal(f.session.snapshot().canRestoreOriginal, true);
  assert.equal(await f.session.restoreOriginal(), true);
  assert.equal(f.input.aiHub.audioPrompt, originalAudio);
  assert.equal(f.input.prompt, "Original text");
  f.session.restore({assistant_original_prompt:"Original text",assistant_original_audio:originalAudio,
    assistant_suggestion:"Prepared proposal",assistant_audio_suggestion:"Improved soundtrack",assistant_action:"applied",correlation_id:id});
  f.input.prompt = "Prepared proposal";
  f.input.aiHub.audioPrompt = "Improved soundtrack";
  assert.equal(await f.session.restoreOriginal(), true);
  assert.equal(f.input.aiHub.audioPrompt, originalAudio);
});

for (const mode of ["text-to-image", "image-to-image", "minimax-h3-video"]) test(`assistant wire contract: ${mode}`, () => {
  const input = { ...baseInput(), mode, videoMode: "references", duration: "12", think: true,
    images: mode === "text-to-image" ? [] : [{ number: 1, value: "u/a.png", local: "preview-a", role: "base_scene" }, { number: 4, value: "u/d.png", local: "preview-d", role: "style" }],
    audio: { value: "u/sound.wav" }, video: { value: "u/motion.mp4" } };
  const body = assistant.buildRequest(input);
  assert.equal(body.get("prompt"), "Original text");
  assert.equal(body.get("template_id"), mode);
  assert.equal(body.get("assistant_think"), "true");
  assert.equal(body.get("assistant_template"), "workflow-default");
  assert.equal(body.has("csrf"), false);
  assert.equal(body.has("model"), false);
  assert.equal(body.has("input_audio"), false);
  assert.equal(body.has("input_video"), false);
  assert.equal(body.get("input_image"), mode === "text-to-image" ? null : "u/a.png");
  assert.equal(body.get("input_image_4"), mode === "text-to-image" ? null : "u/d.png");
  assert.equal(body.get("image_role_4"), mode === "text-to-image" ? null : "style");
  assert.equal(body.get("video_has_audio"), mode === "minimax-h3-video" ? "true" : null);
  assert.equal(body.get("video_has_video"), mode === "minimax-h3-video" ? "true" : null);
  assert.equal(body.get("video_duration_seconds"), mode === "minimax-h3-video" ? "12" : null);
});

test("frame wire omits free-reference roles and stale source corrections", () => {
  const input = { ...baseInput(), mode: "minimax-h3-video", images: [
    { number: 1, value: "first.png", role: "first_frame" }, { number: 4, value: "last.png", role: "last_frame" },
  ] };
  const body = assistant.buildRequest(input, { "Picture 1": { text: "stale", source: "other.png" }, "Picture 2": { text: "Current last frame", source: "last.png" } });
  assert.equal(body.has("image_role_1"), false);
  assert.equal(body.has("image_correction_1"), false);
  assert.equal(body.get("image_correction_4"), "Current last frame");
});

test("assistant catalog choice is sent separately from the generation model", () => {
  const input = { ...baseInput(), assistantModel: "large-text" };
  assert.equal(assistant.buildRequest(input).get("assistant_model"), "large-text");
  assert.equal(assistant.buildRequest(input).has("model"), false);
  assert.equal(assistant.buildRequest(baseInput()).has("assistant_model"), false);
});

test("changing assistant model invalidates a prepared proposal", async () => {
  const f = fixture({ input: { assistantModel: "large-text" } });
  await f.session.improve();
  assert.equal(f.session.snapshot().canApply, true);
  f.input.assistantModel = "vision";
  f.session.syncContext();
  assert.equal(f.session.snapshot().canApply, false);
  assert.equal(f.state().stale, true);
});

for (const changed of [false, true]) test(`lost model receipt preserves choice and recovers separately when changed=${changed}`, async () => {
  let body;
  const f = fixture({ durable: true, input: { assistantModel: "large-text" }, transport: async (op, fields) => {
    if (op === "submit") { body = new URLSearchParams(fields); throw new TypeError("Failed to fetch"); }
    return ok({ id, state: "completed", context: body.get("context"), original: body.get("prompt"), expires_at: new Date(86400000).toISOString(), result: proposal() });
  } });
  await f.session.improve();
  assert.equal(body.get("assistant_model"), "large-text");
  assert.equal(JSON.parse(body.get("context")).assistantModel, "large-text");
  if (changed) { f.input.assistantModel = "vision"; f.session.syncContext(); }
  await f.advance(6000);
  assert.deepEqual(f.calls.map(call => call.op), ["submit", "get"]);
  assert.equal(f.input.draft, changed ? "" : "Prepared proposal");
  assert.equal(f.saved.length, changed ? 1 : 0);
});

test("proposal preparation is coalesced, does not alter the prompt, and needs an acknowledged decision", async () => {
  const wait = deferred();
  const f = fixture({ prepare: () => wait.promise });
  const pending = f.session.improve();
  assert.equal(await f.session.improve(), false);
  assert.equal(f.session.snapshot().busy, true);
  assert.equal(f.calls.length, 0);
  wait.resolve(); await pending;
  assert.equal(f.input.prompt, "Original text");
  assert.equal(f.input.draft, "Prepared proposal");
  assert.equal(f.state().approved, false);
  assert.equal(f.session.snapshot().canApply, true);
  assert.equal(await f.session.apply(), true);
  assert.equal(f.input.prompt, "Prepared proposal");
  assert.equal(f.state().action, "applied");
  assert.deepEqual(f.calls.map(call => call.op), ["legacy", "decision"]);
});

test("a preparation context change prevents the model request", async () => {
  const wait = deferred(), f = fixture({ prepare: () => wait.promise });
  const pending = f.session.improve();
  f.input.workflow = "different-workflow";
  wait.resolve(); await pending;
  assert.equal(f.calls.length, 0);
  assert.equal(f.state().stale, true);
});

for (const kind of ["audio", "video"]) test(`replacing ${kind} with a same-name file invalidates preparation`, async () => {
  const wait = deferred(), f = fixture({ prepare: () => wait.promise,
    input: { mode: "minimax-h3-video", [kind]: { local: { name: "same.ext" }, value: "" } } });
  const pending = f.session.improve();
  f.input[kind] = { local: { name: "same.ext" }, value: "uploaded.ext" };
  wait.resolve(); await pending;
  assert.equal(f.calls.length, 0);
  assert.equal(f.state().stale, true);
});

test("a shared upload preserves local identity while supplying the final source path", async () => {
  const wait = deferred(), source = { name: "sound.wav" };
  const f = fixture({ prepare: () => wait.promise, input: { mode: "minimax-h3-video", audio: { local: source, value: "" } } });
  const pending = f.session.improve();
  f.input.audio.value = "u/sound.wav";
  wait.resolve(); await pending;
  assert.equal(f.calls[0].body.get("video_has_audio"), "true");
  assert.equal(f.state().stale, false);
});

for (const mutation of ["prompt", "draft", "model"]) test(`late legacy proposal cannot replace edited ${mutation}`, async () => {
  const wait = deferred(), f = fixture({ transport: () => wait.promise });
  const pending = f.session.improve(); await tick();
  f.input[mutation] = "My newer value";
  if (mutation === "draft") f.session.editDraft();
  wait.resolve(ok(proposal())); await pending;
  assert.equal(f.applied.length, 0);
  assert.notEqual(f.input.draft, "Prepared proposal");
  assert.equal(f.state().stale, true);
});

test("reset rejects a late proposal and a late error without replacing the new review", async () => {
  for (const reject of [false, true]) {
    const wait = deferred(), f = fixture({ transport: () => wait.promise });
    const pending = f.session.improve(); await tick();
    f.session.reset();
    if (reject) wait.reject(new Error("Old error")); else wait.resolve(ok(proposal()));
    await pending;
    assert.equal(f.state().status, "idle");
    assert.equal(f.input.draft, "");
    assert.equal(f.session.snapshot().busy, false);
  }
});

test("empty/foreign proposal evidence cannot become an applicable review", async () => {
  for (const value of [{ prompt: "" }, { prompt: "Text" }, { prompt: {}, correlation_id: id }]) {
    const f = fixture({ transport: async () => ok(value) });
    await f.session.improve();
    assert.equal(f.state().status, "error");
    assert.equal(f.session.snapshot().canApply, false);
  }
});

test("manual proposal edits use the edited decision and preserve the original wire prompt", async () => {
  const f = fixture(); await f.session.improve();
  f.input.draft = "My reviewed wording"; f.session.editDraft();
  await f.session.apply();
  assert.equal(f.calls[0].body.get("prompt"), "Original text");
  assert.equal(f.calls[1].body.get("decision"), "edited_after_apply");
  assert.equal(f.calls[1].body.get("final_prompt"), "My reviewed wording");
  assert.equal(f.state().action, "edited_after_apply");
});

for (const mutation of ["prompt", "draft", "model"]) test(`late apply acknowledgement preserves newer ${mutation}`, async () => {
  const wait = deferred(), f = fixture({ transport: async op => op === "decision" ? wait.promise : ok(proposal()) });
  await f.session.improve();
  const pending = f.session.apply(); await tick();
  f.input[mutation] = "Changed while saving";
  wait.resolve(ok({ decision: "applied" }));
  assert.equal(await pending, false);
  assert.equal(f.applied.length, 0);
  assert.equal(f.state().approved, false);
});

test("decision failure keeps editor text and a late failure after reset cannot damage new state", async () => {
  const wait = deferred(), f = fixture({ transport: op => op === "decision" ? wait.promise : Promise.resolve(ok(proposal())) });
  await f.session.improve(); const pending = f.session.apply(); await tick();
  wait.resolve(fail("Synthetic save failure")); await pending;
  assert.equal(f.input.prompt, "Original text");
  assert.equal(f.state().status, "error");
  assert.equal(await f.session.beforeLaunch(), false);
  const late = deferred(), g = fixture({ transport: op => op === "decision" ? late.promise : Promise.resolve(ok(proposal())) });
  await g.session.improve(); const old = g.session.apply(); await tick();
  g.session.reset(); late.resolve(fail("Old failure")); await old;
  assert.equal(g.state().status, "idle");
});

test("another decision and another model request cannot overlap an apply acknowledgement", async () => {
  const wait = deferred(), f = fixture({ transport: op => op === "decision" ? wait.promise : Promise.resolve(ok(proposal())) });
  await f.session.improve(); const pending = f.session.apply(); await tick();
  assert.equal(await f.session.keep(), false);
  assert.equal(await f.session.apply(), false);
  assert.equal(await f.session.improve(), false);
  assert.equal(f.calls.length, 2);
  wait.resolve(ok({ decision: "applied" })); await pending;
});

test("serialized prompt edits save the newest text last and suppress an obsolete failure", async () => {
  const first = deferred(); let edits = 0;
  const f = fixture({ transport: async (op, body) => op !== "decision" ? ok(proposal()) : body.get("decision") !== "edited_after_apply" ? ok({ decision: "applied" }) : ++edits === 1 ? first.promise : ok({ decision: "edited_after_apply" }) });
  await f.session.improve(); await f.session.apply();
  f.input.prompt = "First edit"; f.session.editPrompt(); const a = f.session.savePromptEdit(); await tick();
  f.input.prompt = "Second edit"; f.session.editPrompt(); const b = f.session.savePromptEdit(); await tick();
  assert.equal(edits, 1);
  first.resolve(fail("Obsolete save failed")); await a; await b;
  assert.equal(edits, 2);
  assert.equal(f.calls.at(-1).body.get("final_prompt"), "Second edit");
  assert.equal(f.state().approved, true);
});

test("launch waits for decision persistence and refuses edits during the final acknowledgement", async () => {
  const wait = deferred(); let count = 0;
  const f = fixture({ transport: async op => op !== "decision" ? ok(proposal()) : ++count === 1 ? ok({ decision: "applied" }) : wait.promise });
  await f.session.improve(); await f.session.apply();
  const pending = f.session.beforeLaunch(); await tick();
  f.input.prompt = "New text after launch click"; f.session.editPrompt();
  wait.resolve(ok({ decision: "edited_after_apply" }));
  assert.equal(await pending, false);
  assert.equal(f.state().stale, true);
});

test("wrong decision acknowledgement is not considered consent", async () => {
  const f = fixture({ transport: async op => ok(op === "decision" ? { decision: "kept_original" } : proposal()) });
  await f.session.improve();
  assert.equal(await f.session.apply(), false);
  assert.equal(f.input.prompt, "Original text");
  assert.equal(f.state().approved, false);
});

test("keeping an outdated proposal retains the current editor and does not save a foreign decision", async () => {
  const f = fixture(); await f.session.improve();
  f.input.model = "changed-model"; f.session.syncContext();
  assert.equal(await f.session.keep(), true);
  assert.equal(f.input.prompt, "Original text");
  assert.equal(f.state().action, "kept_original");
  assert.equal(f.state().correlationID, "");
  assert.deepEqual(f.calls.map(call => call.op), ["legacy"]);
});

test("restoring the original preserves the proposal and can apply it again", async () => {
  const f = fixture(); await f.session.improve(); await f.session.apply();
  assert.equal(f.session.snapshot().canRestoreOriginal, true);
  assert.equal(await f.session.restoreOriginal(), true);
  assert.equal(f.input.prompt, "Original text");
  assert.equal(f.input.draft, "Prepared proposal");
  assert.equal(f.state().action, "kept_original");
  assert.equal(f.session.snapshot().hasOriginal, false);
  assert.equal(f.calls.at(-1).body.get("decision"), "kept_original");
  assert.equal(f.calls.at(-1).body.get("final_prompt"), "Original text");
  assert.equal(await f.session.apply(), true);
  assert.equal(f.input.prompt, "Prepared proposal");
});

test("restoring queues after a blur save and blocks duplicate restores", async () => {
  const wait = deferred();
  const f = fixture({ transport: async (op, body) => op !== "decision" ? ok(proposal())
    : body.get("decision") === "edited_after_apply" ? wait.promise : ok({ decision: body.get("decision") }) });
  await f.session.improve(); await f.session.apply();
  f.input.prompt = "My latest edit"; f.session.editPrompt();
  const edit = f.session.savePromptEdit(); await tick();
  assert.equal(f.session.snapshot().canRestoreOriginal, true);
  const restore = f.session.restoreOriginal();
  assert.equal(await f.session.restoreOriginal(), false);
  assert.equal(f.input.prompt, "My latest edit");
  wait.resolve(ok({ decision: "edited_after_apply" })); await edit;
  assert.equal(await restore, true);
  assert.equal(f.input.prompt, "Original text");
  assert.deepEqual(f.calls.filter(call => call.op === "decision").map(call => call.body.get("decision")), ["applied", "edited_after_apply", "kept_original"]);
});

test("launch waits for original restoration and persists the restored choice", async () => {
  const wait = deferred(); let restores = 0;
  const f = fixture({ transport: async (op, body) => op !== "decision" ? ok(proposal())
    : body.get("decision") === "kept_original" && ++restores === 1 ? wait.promise : ok({ decision: body.get("decision") }) });
  await f.session.improve(); await f.session.apply();
  const restore = f.session.restoreOriginal(); await tick();
  let launched = false;
  const launch = f.session.beforeLaunch().then(allowed => { launched = allowed; return allowed; });
  await tick(); assert.equal(launched, false);
  wait.resolve(ok({ decision: "kept_original" }));
  assert.equal(await restore, true);
  assert.equal(await launch, true);
  assert.equal(f.input.prompt, "Original text");
  assert.equal(f.state().action, "kept_original");
  assert.equal(f.state().stale, false);
  assert.equal(f.calls.at(-1).body.get("final_prompt"), "Original text");
});

test("failed original restoration preserves text and permits explicit retry", async () => {
  let failed = true;
  const f = fixture({ transport: async (op, body) => op !== "decision" ? ok(proposal())
    : body.get("decision") === "kept_original" && failed ? fail("Save unavailable") : ok({ decision: body.get("decision") }) });
  await f.session.improve(); await f.session.apply();
  assert.equal(await f.session.restoreOriginal(), false);
  assert.equal(f.input.prompt, "Prepared proposal");
  assert.equal(f.session.snapshot().canRestoreOriginal, true);
  failed = false;
  assert.equal(await f.session.restoreOriginal(), true);
  assert.equal(f.input.prompt, "Original text");
});

test("late original restoration never replaces newer edits or another context", async () => {
  for (const change of [f => { f.input.prompt = "Newer text"; f.session.editPrompt(); }, f => f.session.reset(), f => f.session.suspend()]) {
    const wait = deferred();
    const f = fixture({ transport: async (op, body) => op !== "decision" ? ok(proposal())
      : body.get("decision") === "kept_original" ? wait.promise : ok({ decision: body.get("decision") }) });
    await f.session.improve(); await f.session.apply();
    const restore = f.session.restoreOriginal(); await tick(); change(f);
    const text = f.input.prompt;
    wait.resolve(ok({ decision: "kept_original" }));
    assert.equal(await restore, false);
    assert.equal(f.input.prompt, text);
  }
});

test("saved applied drafts retain the original restoration action", async () => {
  const f = fixture({ initialize: false, input: { prompt: "Prepared proposal" } });
  f.session.restore({ assistant_original_prompt: "Original text", assistant_suggestion: "Prepared proposal", assistant_action: "applied", correlation_id: id });
  f.session.initialize();
  assert.equal(f.session.snapshot().canRestoreOriginal, true);
  assert.equal(await f.session.restoreOriginal(), true);
  assert.equal(f.input.prompt, "Original text");
});

test("empty or unapplied originals do not offer restoration", async () => {
  const f = fixture(); await f.session.improve();
  assert.equal(f.session.snapshot().canRestoreOriginal, false);
  assert.equal(await f.session.restoreOriginal(), false);
  const g = fixture({ input: { prompt: "", mode: "minimax-h3-video", images: [{ number: 1, value: "u/one.png" }] } });
  await g.session.improve(); await g.session.apply();
  assert.equal(g.session.snapshot().hasOriginal, false);
});

test("default protocol uses durable jobs and one POST after a lost receipt", async () => {
  let body;
  const f = fixture({ defaultProtocol: true, transport: async (op, fields) => {
    if (op === "submit") { body = new URLSearchParams(fields); throw new Error("Lost receipt"); }
    return ok({ id, state: "completed", context: body.get("context"), original: body.get("prompt"), expires_at: new Date(86400000).toISOString(), result: proposal() });
  } });
  await f.session.improve();
  assert.equal(body.get("context"), JSON.stringify({ workflow: "photoflow-krea2", model: "krea2.safetensors", mode: "text-to-image", videoMode: "frames", duration: "5", profile: "workflow-default", think: false, images: [], audio: "", video: "", corrections: [] }));
  await f.advance(6000);
  assert.deepEqual(f.calls.map(call => call.op), ["submit", "get"]);
  assert.equal(f.input.draft, "Prepared proposal");
  assert.equal(f.state().approved, false);
});

test("a restored durable result stays separate from manual draft edits", async () => {
  const f = fixture({ initialize: false, durable: true, transport: async () => ok({ id, state: "completed", context: "old-context", original: "Original text", result: proposal("Server answer") }) });
  f.session.restore({ assistant_job_id: id, assistant_original_prompt: "Original text", assistant_suggestion: "Older answer", assistant_draft: "My manual draft", assistant_draft_edited: "true" });
  f.session.initialize(); await tick();
  assert.equal(f.input.draft, "My manual draft");
  assert.equal(f.saved[0].prompt, "Server answer");
  assert.equal(f.session.snapshot().canApply, false);
  assert.deepEqual(f.calls.map(call => call.op), ["get"]);
});

test("restored approval and source corrections remain bound without another model call", async () => {
  const f = fixture({ initialize: false });
  f.session.restore({ assistant_original_prompt: "Original text", assistant_suggestion: "Saved suggestion", assistant_draft: "Edited saved wording", correlation_id: id, assistant_action: "edited_after_apply", assistant_draft_edited: "true",
    assistant_references: JSON.stringify([{ id: "image 1", role: "identity", source_value: "a.png" }]), assistant_corrections: JSON.stringify({ "image 1": { source: "a.png", text: "Current correction" }, invalid: { source: 3, text: false } }) });
  f.session.initialize();
  assert.equal(f.input.draft, "Edited saved wording");
  assert.equal(f.state().approved, true);
  assert.equal(f.state().draftEdited, true);
  assert.deepEqual(f.session.corrections(), { "image 1": { source: "a.png", text: "Current correction" } });
  assert.equal(f.calls.length, 0);
});

test("suspending a legacy request ignores its late response even after resuming", async () => {
  const wait = deferred(), f = fixture({ transport: () => wait.promise });
  const pending = f.session.improve(); await tick();
  f.session.suspend(); f.session.resume();
  wait.resolve(ok(proposal())); await pending;
  assert.equal(f.input.draft, "");
  assert.equal(f.calls.length, 1);
  assert.equal(f.calls[0].signal.aborted, true);
});

test("unrestored or invalid browser persistence cannot silently approve launch", async () => {
  const f = fixture({ initialize: false });
  assert.equal(await f.session.beforeLaunch(), false);
  const storage = new Map([["ai_gateway_assistant_job_v1_1", "{bad-json"]]);
  const g = fixture({ storage });
  assert.equal(g.session.snapshot().ready, false);
  assert.equal(await g.session.improve(), false);
  assert.equal(await g.session.beforeLaunch(), false);
  assert.equal(g.calls.length, 0);
});

test("pointer writes are account-scoped, contain no draft and do not dirty an unchanged poll", () => {
  const f = fixture(), writes = [];
  const persistence = assistant.createPersistence({ storage: () => f.target, owner: "2", validID: jobs.validID, onChange: () => writes.push(1) });
  persistence.persist({ id, expiresAt: 500000, cancelling: false, prompt: "Never persist this" });
  persistence.persist({ id, expiresAt: 500000, cancelling: false });
  assert.equal(writes.length, 1);
  assert.equal(f.storage.has("ai_gateway_assistant_job_v1_1"), false);
  assert.equal(f.storage.get("ai_gateway_assistant_job_v1_2").includes("Never"), false);
  assert.equal(persistence.read().id, id);
  persistence.persist(null);
  assert.equal(persistence.read(), null);
});

test("HTTP boundary copies fields, scopes credentials and never gives GET a body", async () => {
  const calls = [], signal = new AbortController().signal;
  const transport = assistant.createTransport({ csrf: () => "current-csrf", timeoutSignal: () => signal,
    fetcher: async (url, options) => { calls.push({ url, options }); return { ok: true, status: 200, json: async () => ({}) }; } });
  const fields = new URLSearchParams({ id, prompt: "Source prompt" });
  for (const op of ["legacy", "submit", "get", "cancel", "decision"]) await transport(op, fields, signal);
  assert.equal(fields.has("csrf"), false);
  assert.equal(calls[2].url, `/generate/prompt-assistant/jobs?id=${id}`);
  assert.equal(calls[2].options.body, undefined);
  for (const call of calls) {
    assert.equal(call.options.cache, "no-store");
    assert.equal(call.options.credentials, "same-origin");
    assert.equal(call.options.redirect, "error");
    if (call.options.body) assert.equal(call.options.body.get("csrf"), "current-csrf");
  }
  await assert.rejects(transport("unknown", fields, signal), /Unknown/);
});
