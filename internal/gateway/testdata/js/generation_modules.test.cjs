const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

const storeModule = require("../../static/generation-store.js");
const wizard = require("../../static/generation-wizard.js");
const media = require("../../static/generation-media.js");
const summary = require("../../static/generation-summary.js");
const video = require("../../static/generation-video.js");
const assistant = require("../../static/generation-assistant.js");
const batch = require("../../static/generation-batch.js");
const job = require("../../static/generation-job.js");
const recipes = require("../../static/generation-recipes.js");
const history = require("../../static/generation-history.js");
const lightbox = require("../../static/generation-lightbox.js");
const dialogFocus = require("../../static/dialog-focus.js");

test("generation store publishes named and global changes", () => {
  const store = storeModule.createStore({ wizard: { step: 1 } });
  const events = [];
  const unsubscribe = store.subscribe("wizard:change", (change) => events.push(change));
  store.setSlice("wizard", { step: 2 }, "wizard:change");
  unsubscribe();
  store.setSlice("wizard", { step: 3 }, "wizard:change");
  assert.equal(store.getSlice("wizard").step, 3);
  assert.equal(events.length, 1);
  assert.equal(events[0].previous.step, 1);
  assert.equal(events[0].current.step, 2);
});

test("wizard switches scenarios without browser state", () => {
  let state = wizard.createState();
  state = wizard.reduce(state, {
    type: "SELECT_SCENARIO",
    scenarioID: "image-to-image",
    requiresImage: true,
    allowsImages: true,
  });
  state = wizard.reduce(state, { type: "SELECT_WORKFLOW", workflowID: "krea-edit", available: true });
  state = wizard.reduce(state, { type: "SET_SELECTIONS", selectedCount: 1, primarySelected: true, pendingUploads: 1 });
  assert.equal(state.step, 2);
  assert.equal(wizard.canContinue(state), true);
  assert.equal(wizard.nextActionLabel(state), "upload");
  state = wizard.reduce(state, { type: "UPLOAD_START" });
  assert.equal(wizard.canContinue(state), false);
  state = wizard.reduce(state, { type: "UPLOAD_FINISH" });
  assert.equal(wizard.canContinue(state), true);

  state = wizard.reduce(state, {
    type: "SELECT_SCENARIO",
    scenarioID: "text-to-image",
    requiresImage: false,
    allowsImages: false,
  });
  assert.equal(state.workflowID, "");
  assert.equal(state.workflowAvailable, false);
  assert.equal(state.requiresImage, false);
  assert.equal(wizard.canContinue(state), false);
});

test("media exposes one source interface for device and gallery", async () => {
  const file = { name: "source.png", size: 42, type: "image/png" };
  const galleryEntry = { id: 17, filename: "saved.png", url: "/media/17" };
  assert.equal(media.sourceFrom(file, galleryEntry).kind, media.SOURCE_DEVICE);
  assert.equal(media.sourceFrom(null, galleryEntry).kind, media.SOURCE_GALLERY);

  const requests = [];
  const fakeResponse = { ok: true, json: async () => ({ name: "ready.png", subfolder: "users/u1" }) };
  const deviceBody = { values: [], append(...args) { this.values.push(args); } };
  const deviceResult = await media.uploadImageSource(media.deviceSource(file), {
    fetcher: async (url, options) => { requests.push({ url, options }); return fakeResponse; },
    formDataFactory: () => deviceBody,
  });
  assert.equal(deviceResult.value, "users/u1/ready.png");
  assert.equal(requests[0].url, "/generate/upload/image");
  assert.deepEqual(deviceBody.values.map(([name]) => name), ["image", "type", "overwrite"]);

  const galleryBody = { kind: "search-params" };
  await media.uploadImageSource(media.gallerySource(galleryEntry), {
    csrf: "token",
    fetcher: async (url, options) => { requests.push({ url, options }); return fakeResponse; },
    searchParamsFactory: (values) => Object.assign(galleryBody, values),
  });
  assert.equal(requests[1].url, "/generate/library/reuse-image");
  assert.equal(galleryBody.media_id, "17");
  assert.equal(galleryBody.csrf, "token");

  let state = media.createState();
  state = media.reduce(state, { type: "UPLOAD_START" });
  state = media.reduce(state, { type: "SELECT_SOURCE", slot: 1, source: media.gallerySource(galleryEntry) });
  state = media.reduce(state, { type: "UPLOAD_SUCCESS", slot: 1, value: "users/u1/ready.png" });
  assert.equal(state.uploading, true);
  state = media.reduce(state, { type: "UPLOAD_FINISH" });
  assert.equal(state.sources[1].kind, media.SOURCE_GALLERY);
  assert.equal(state.uploaded[1], "users/u1/ready.png");
  assert.equal(state.uploading, false);
});

test("media keeps the role and source of every reference", () => {
  const file = { name: "device.png", size: 42, type: "image/png" };
  const galleryEntry = { id: 17, filename: "gallery.png", url: "/media/17" };
  assert.deepEqual(
    media.referenceMetadata({ templateID: "image-to-image", slot: 1, source: media.deviceSource(file), role: "identity" }),
    { number: 1, role: "base_scene", source: "device", sourceID: "", sourceName: "device.png" },
  );
  assert.deepEqual(
    media.referenceMetadata({ templateID: "image-to-image", slot: 2, source: media.gallerySource(galleryEntry), role: "identity" }),
    { number: 2, role: "identity", source: "gallery", sourceID: "17", sourceName: "gallery.png" },
  );
  assert.equal(media.referenceMetadata({ templateID: "minimax-h3-video", videoMode: "frames", slot: 1, uploaded: "restored/first.png" }).role, "first_frame");
  assert.equal(media.referenceMetadata({ templateID: "minimax-h3-video", videoMode: "frames", slot: 2, uploaded: "restored/last.png" }).role, "last_frame");
  assert.deepEqual(
    media.referenceMetadata({ templateID: "minimax-h3-video", videoMode: "references", slot: 4, role: "style", uploaded: "restored/style.png" }),
    { number: 4, role: "style", source: "restored", sourceID: "", sourceName: "restored/style.png" },
  );
});

test("generation guides cover every quick-generation family and video mode", () => {
  assert.equal(summary.guideFor({ family: "krea2", templateID: "text-to-image" }).eyebrow, "Текст в изображение");
  assert.equal(summary.guideFor({ family: "krea2", templateID: "image-to-image" }).eyebrow, "Редактирование фото");
  assert.equal(summary.guideFor({ family: "flux2", templateID: "image-to-image" }).eyebrow, "Точное редактирование");
  assert.equal(summary.modeLabel({ family: "minimax_h3", templateID: "minimax-h3-video", references: [] }), "Текст в видео");
  assert.equal(summary.modeLabel({ family: "minimax_h3", templateID: "minimax-h3-video", references: [{ number: 1 }] }), "Первый кадр");
  assert.equal(summary.modeLabel({ family: "minimax_h3", templateID: "minimax-h3-video", references: [{ number: 1 }, { number: 2 }] }), "Первый и последний кадры");
  assert.equal(summary.modeLabel({ family: "minimax_h3", templateID: "minimax-h3-video", videoMode: "references" }), "Свободные референсы");
});

test("launch summary reports shared settings and heavy work for all families", () => {
  const imageSummary = summary.buildSummary({
    family: "flux2", templateID: "image-to-image", workflowName: "Flux2 Редактирование", modelName: "Flux2 Dev",
    output: "2048 × 2048", references: [{ number: 1, source: "gallery" }, { number: 2, source: "device" }],
    loraCount: 2, heavyOptions: ["SeedVR2"],
  });
  assert.equal(imageSummary.facts.find((item) => item.label === "Режим").value, "Точное редактирование");
  assert.equal(imageSummary.facts.find((item) => item.label === "Материалы").value, "2 фото (1 из галереи, 1 с устройства)");
  assert.equal(imageSummary.load, "high");
  assert.match(imageSummary.impact, /SeedVR2/);

  const videoSummary = summary.buildSummary({
    family: "minimax_h3", templateID: "minimax-h3-video", workflowName: "MiniMaxH3 Видео", modelName: "MiniMax H3 v4",
    duration: "10 секунд", output: "480p", videoMode: "references", hasAudio: true,
  });
  assert.equal(videoSummary.facts.find((item) => item.label === "Режим").value, "Свободные референсы");
  assert.equal(videoSummary.facts.find((item) => item.label === "Результат").value, "480p · 10 секунд");
});

test("video state derives modes, profiles, limits, and reference resolution", () => {
  let state = video.createState();
  state = video.reduce(state, { type: "SET_MODE", mode: "references" });
  state = video.reduce(state, { type: "SET_PROFILE", profileID: "turbo" });
  assert.deepEqual(state, { mode: "references", profileID: "turbo" });
  assert.equal(video.activeImageLimit({ isMiniMax: true, mode: "frames", maxInputImages: 4 }), 2);
  assert.equal(video.activeImageLimit({ isMiniMax: true, mode: "references", maxInputImages: 4 }), 4);
  assert.equal(video.referencesAvailable({ isMiniMax: true, mode: "references" }), true);
  assert.equal(video.profileID({ integratedTurbo: true, turbo: true }), "integrated_turbo");
  assert.deepEqual(
    video.scaledResolution({ sourceSize: { width: 1408, height: 1872 }, maxResolution: 480 }),
    { width: 352, height: 480, sourceWidth: 1408, sourceHeight: 1872 },
  );
});

test("assistant state keeps prompt lineage through review and edits", () => {
  let state = assistant.reduce(undefined, { type: "REQUEST_START", original: "Draft" });
  state = assistant.reduce(state, { type: "REQUEST_SUCCESS", suggestion: "Improved", correlationID: "corr-1" });
  state = assistant.reduce(state, { type: "APPLY" });
  state = assistant.reduce(state, { type: "PROMPT_EDITED" });
  assert.equal(state.original, "Draft");
  assert.equal(state.suggestion, "Improved");
  assert.equal(state.correlationID, "corr-1");
  assert.equal(state.action, "edited_after_apply");
  assert.equal(state.approved, true);
});

test("generation batches expose controlled parameters for every supported family", () => {
  const krea = batch.parameterOptions({ family: "krea2", templateID: "image-to-image", loras: ["detail.safetensors"] });
  assert.ok(krea.some((item) => item.name === "reference_boost"));
  assert.ok(krea.some((item) => item.name === "lora_model_strength_1"));
  assert.equal(krea.some((item) => item.name === "flux_guidance"), false);

  const flux = batch.parameterOptions({ family: "flux2", templateID: "image-to-image" });
  assert.equal(flux.find((item) => item.name === "flux_guidance").max, 10);
  assert.equal(flux.find((item) => item.name === "flux_active_scale").max, 10);

  const minimax = batch.parameterOptions({
    family: "minimax_h3", templateID: "minimax-h3-video", rife: true, rtx: true,
    colorMatch: true, sharpen: true, sharpenMax: 3,
  });
  assert.ok(minimax.some((item) => item.name === "video_rife_multiplier"));
  assert.ok(minimax.some((item) => item.name === "video_rtx_scale"));
  assert.equal(minimax.find((item) => item.name === "video_sharpen_strength").max, 3);
});

test("generation batches enforce quota, distinct values, and two-result comparison", () => {
  const options = batch.parameterOptions({ family: "krea2", templateID: "text-to-image" });
  assert.equal(batch.validate({ enabled: true, mode: "seeds", count: 4 }, options, { totalLimit: 3, totalRemaining: 3 }).valid, false);
  assert.match(
    batch.validate({ enabled: true, mode: "parameter", count: 4, parameter: "steps", from: "8", to: "9" }, options).error,
    /слишком узкий/i,
  );
  assert.equal(batch.validate({ enabled: true, mode: "parameter", count: 4, parameter: "steps", from: "8", to: "11" }, options).valid, true);

  const batchItem = {
    batch_id: "batch-1",
    jobs: [{ job_id: "a" }, { job_id: "b" }, { job_id: "c" }],
    differences: [{ name: "steps", label: "Шаги", values: [
      { job_id: "a", position: 1, value: "8" },
      { job_id: "b", position: 2, value: "9" },
      { job_id: "c", position: 3, value: "10" },
    ] }],
  };
  let state = batch.createState({ batches: [batchItem] });
  state = batch.reduce(state, { type: "TOGGLE_COMPARE", batchID: "batch-1", jobID: "a" });
  state = batch.reduce(state, { type: "TOGGLE_COMPARE", batchID: "batch-1", jobID: "b" });
  state = batch.reduce(state, { type: "TOGGLE_COMPARE", batchID: "batch-1", jobID: "c" });
  assert.deepEqual(state.selections["batch-1"], ["b", "c"]);
  assert.deepEqual(batch.selectedDifferences(state, batchItem)[0].values.map((item) => item.value), ["9", "10"]);
});

test("job state never moves its SSE revision backwards", () => {
  let state = job.reduce(undefined, { type: "LOAD_START" });
  state = job.reduce(state, { type: "SET_JOBS", items: [{ job_id: "a" }], revision: 12 });
  state = job.reduce(state, { type: "SET_REVISION", revision: 7 });
  state = job.reduce(state, { type: "SET_LIVE", live: true });
  assert.equal(state.loading, false);
  assert.equal(state.items.length, 1);
  assert.equal(state.revision, 12);
  assert.equal(state.live, true);
});

test("recipe state preserves a valid selection and clears a stale one", () => {
  let state = recipes.createState({ selectedID: "2" });
  state = recipes.reduce(state, { type: "SET_ITEMS", items: [{ id: 1 }, { id: 2 }] });
  assert.equal(recipes.selectedRecipe(state).id, 2);
  state = recipes.reduce(state, { type: "SET_ITEMS", items: [{ id: 1 }] });
  assert.equal(state.selectedID, "");
});

test("history state filters jobs and toggles its compact view", () => {
  let state = history.createState();
  state = history.reduce(state, { type: "SET_FILTERS", stateFilter: "completed", templateFilter: "minimax-h3-video" });
  const filtered = history.filterJobs([
    { state: "completed", template_id: "minimax-h3-video" },
    { state: "failed", template_id: "minimax-h3-video" },
    { state: "completed", template_id: "text-to-image" },
  ], state);
  state = history.reduce(state, { type: "TOGGLE_COLLAPSED" });
  assert.equal(filtered.length, 1);
  assert.equal(state.collapsed, true);
});

test("lightbox state and download links distinguish media", () => {
  let state = lightbox.reduce(undefined, { type: "OPEN", output: { media_type: "video", url: "/media/a.mp4", filename: "a.mp4" } });
  assert.equal(state.open, true);
  assert.equal(state.mediaType, "video");
  assert.equal(lightbox.downloadURL("/media/a.mp4?token=x", "https://gateway.test"), "/media/a.mp4?token=x&download=1");
  state = lightbox.reduce(state, { type: "CLOSE" });
  assert.deepEqual(state, lightbox.createState());
});

test("dialog focus trap cycles, closes and restores the trigger", () => {
  let keydown = null;
  const documentObject = {
    activeElement: null,
    addEventListener: (_type, listener) => { keydown = listener; },
    removeEventListener: (_type, listener) => { if (keydown === listener) keydown = null; },
  };
  const element = () => ({
    disabled: false,
    hidden: false,
    tabIndex: 0,
    isConnected: true,
    getAttribute: () => null,
    getClientRects: () => [{}],
    focus() { documentObject.activeElement = this; },
  });
  const trigger = element();
  const first = element();
  const last = element();
  const root = { hidden: false, querySelectorAll: () => [first, last] };
  const trap = dialogFocus.createFocusTrap({ root, documentRef: documentObject });
  documentObject.activeElement = trigger;
  trap.activate({ trigger, initialFocus: last, onEscape: () => trap.deactivate() });
  assert.equal(documentObject.activeElement, last);

  keydown({ key: "Tab", shiftKey: false, preventDefault() {}, stopPropagation() {} });
  assert.equal(documentObject.activeElement, first);
  keydown({ key: "Escape", preventDefault() {}, stopPropagation() {} });
  assert.equal(documentObject.activeElement, trigger);
  assert.equal(trap.isActive(), false);
});

class FakeClassList {
  constructor() { this.values = new Set(); }
  add(...values) { values.forEach((value) => this.values.add(value)); }
  remove(...values) { values.forEach((value) => this.values.delete(value)); }
  contains(value) { return this.values.has(value); }
  toggle(value, force) {
    const enabled = force === undefined ? !this.values.has(value) : Boolean(force);
    if (enabled) this.values.add(value); else this.values.delete(value);
    return enabled;
  }
}

class FakeElement {
  constructor(id = "") {
    this.id = id;
    this.dataset = {};
    this.classList = new FakeClassList();
    this.style = { setProperty() {} };
    this.hidden = false;
    this.disabled = false;
    this.checked = false;
    this.open = false;
    this.value = "";
    this.textContent = "";
    this.innerHTML = "";
    this.files = [];
    this.options = [];
    this.selectedOptions = [{ value: "", dataset: {} }];
  }
  addEventListener() {}
  removeEventListener() {}
  append() {}
  prepend() {}
  replaceChildren() {}
  removeAttribute() {}
  setAttribute() {}
  getAttribute() { return null; }
  querySelector() { return null; }
  querySelectorAll() { return []; }
  closest() { return null; }
  focus() {}
  scrollIntoView() {}
  requestSubmit() {}
  load() {}
  pause() {}
  play() { return Promise.resolve(); }
  getBoundingClientRect() { return { left: 0, width: 24 }; }
}

const moduleAPIs = { store: storeModule, wizard, media, summary, video, assistant, batch, job, recipes, history, lightbox };

const pageContextWithout = (omittedModule) => {
  const elements = new Map();
  const element = (id) => {
    if (!elements.has(id)) elements.set(id, new FakeElement(id));
    return elements.get(id);
  };
  const controls = new Map();
  const control = (name) => {
    if (!controls.has(name)) controls.set(name, new FakeElement(name));
    return controls.get(name);
  };
  control("video_mode").value = "frames";
  const form = element("generation-form");
  form.elements = new Proxy({ namedItem: (name) => control(name) }, {
    get(target, property) {
      if (property in target) return target[property];
      return control(String(property));
    },
  });
  const root = element("generation-root");
  root.dataset = { selectedWorkflow: "", previewOutput: "", generationRetention: "24 часа", mediaRetention: "24 часа" };
  root.querySelector = () => null;
  root.querySelectorAll = () => [];
  const documentObject = {
    body: element("body"),
    querySelector: (selector) => selector === "[data-comfy-generation]" ? root : null,
    querySelectorAll: () => [],
    getElementById: (id) => id === "generation-form" ? form : element(id),
    createElement: (tag) => element(`created-${tag}-${elements.size}`),
    createDocumentFragment: () => ({ append() {} }),
    addEventListener() {},
  };
  const modules = Object.fromEntries(Object.entries(moduleAPIs).filter(([name]) => name !== omittedModule));
  const windowObject = {
    AIGatewayGeneration: modules,
    location: { origin: "http://gateway.test", href: "http://gateway.test/generate", search: "" },
    history: { replaceState() {} },
    localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
    crypto: { randomUUID: () => "request-id" },
    innerWidth: 1280,
    scrollTo() {},
    addEventListener() {},
    setTimeout,
    setInterval: () => 1,
    clearInterval() {},
    requestAnimationFrame: (callback) => callback(),
  };
  class FakeEventSource { addEventListener() {} close() {} }
  class FakeOption { constructor(text, value) { this.text = text; this.value = value; this.dataset = {}; } }
  const fetcher = async (url) => ({
    ok: true,
    json: async () => {
      if (String(url).includes("capabilities")) return { workflows: [] };
      if (String(url).includes("recipes")) return { recipes: [] };
      if (String(url).includes("variants")) return { variants: [] };
      if (String(url).includes("jobs")) return { jobs: [], revision: 0 };
      return {};
    },
  });
  return {
    root,
    context: {
      window: windowObject,
      document: documentObject,
      console,
      fetch: fetcher,
      URL,
      URLSearchParams,
      FormData,
      EventSource: FakeEventSource,
      Option: FakeOption,
      HTMLElement: FakeElement,
      RadioNodeList: class {},
      CSS: { escape: (value) => String(value) },
      setTimeout,
      clearTimeout,
      Intl,
    },
  };
};

test("generation page initializes when any optional module is absent", async (t) => {
  const source = fs.readFileSync(path.join(__dirname, "../../static/generate.js"), "utf8");
  for (const moduleName of Object.keys(moduleAPIs)) {
    await t.test(`without ${moduleName}`, async () => {
      const { context, root } = pageContextWithout(moduleName);
      vm.runInNewContext(source, context, { filename: "generate.js" });
      await new Promise((resolve) => setTimeout(resolve, 0));
      assert.equal(root.dataset.generationClient, "modular");
      assert.equal(root.dataset.generationModules.split(",").includes(moduleName), false);
    });
  }
});
