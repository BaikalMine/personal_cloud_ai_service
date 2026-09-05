// DOM interaction tests only. jsdom does not validate rendered layout.
const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const {
  JSDOM,
} = require("../../../node_modules/.design-verify/node_modules/jsdom");
const html = fs.readFileSync(path.join(__dirname, "index.html"), "utf8");
const code = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
const lucide = fs.readFileSync(
  path.join(__dirname, "assets/lucide.js"),
  "utf8",
);

function setup() {
  const dom = new JSDOM(html, {
    url: "https://prototype.invalid/",
    runScripts: "outside-only",
    pretendToBeVisual: true,
  });
  const w = dom.window;
  w.matchMedia = () => ({ matches: false, addEventListener() {} });
  w.scrollTo = () => {};
  w.HTMLDialogElement.prototype.showModal = function () {
    this.open = true;
  };
  w.HTMLDialogElement.prototype.close = function () {
    this.open = false;
    this.dispatchEvent(new w.Event("close"));
  };
  w.eval(lucide);
  w.eval(code);
  return {
    w,
    q: (selector) => w.document.querySelector(selector),
    all: (selector) => [...w.document.querySelectorAll(selector)],
    click(selector) {
      const el = w.document.querySelector(selector);
      assert.ok(el, selector);
      el.click();
    },
    change(selector, value) {
      const el = w.document.querySelector(selector);
      assert.ok(el, selector);
      el.value = value;
      el.dispatchEvent(new w.Event("change", { bubbles: true }));
    },
    input(selector, value) {
      const el = w.document.querySelector(selector);
      assert.ok(el, selector);
      el.value = value;
      el.dispatchEvent(new w.Event("input", { bubbles: true }));
    },
    async page(name) {
      w.location.hash = name;
      await new Promise((resolve) => setTimeout(resolve, 15));
    },
    close() {
      w.close();
    },
  };
}

test("all five routes render, icons exist, ids and form labels are valid", async () => {
  const t = setup();
  try {
    for (const route of ["studio", "library", "jobs", "training", "users"]) {
      await t.page(route);
      assert.ok(t.q("h1").textContent.length);
      assert.ok(t.all("svg").length > 8);
      assert.equal(t.all("i[data-lucide]").length, 0, route);
      const ids = t.all("[id]").map((el) => el.id);
      assert.equal(ids.length, new Set(ids).size, route + ": unique ids");
      for (const label of t.all("label[for]"))
        assert.ok(t.w.document.getElementById(label.htmlFor), label.htmlFor);
    }
  } finally {
    t.close();
  }
});

test("light, dark and system theme preferences update and persist", () => {
  const t = setup();
  try {
    t.change("#theme", "dark");
    assert.equal(t.q("html").dataset.theme, "dark");
    assert.equal(t.w.localStorage.getItem("nd-design-theme"), "dark");
    t.click('[data-action="theme"]');
    assert.equal(t.q("html").dataset.theme, "light");
    t.change("#theme", "system");
    assert.equal(t.q("html").dataset.theme, "light");
  } finally {
    t.close();
  }
});

test("prompt edits persist across routes and review requires explicit apply", async () => {
  const t = setup();
  try {
    const prompt = "A scene with <lights> & details";
    t.input("#prompt", prompt);
    await t.page("library");
    await t.page("studio");
    assert.equal(t.q("#prompt").value, prompt);
    t.click('[data-action="assistant"]');
    t.input("#assistant-draft", "Reviewed prompt");
    t.click('[data-action="close"]');
    assert.equal(t.q("#prompt").value, prompt);
    t.click('[data-action="assistant"]');
    t.input("#assistant-draft", "Reviewed prompt");
    t.click('[data-action="apply-prompt"]');
    assert.equal(t.q("#prompt").value, "Reviewed prompt");
    assert.equal(
      JSON.parse(t.w.localStorage.getItem("nd-design-prompt")),
      "Reviewed prompt",
    );
  } finally {
    t.close();
  }
});

test("video exact/free modes, reference picker and independent processing", () => {
  const t = setup();
  try {
    t.click('[data-kind="video"]');
    assert.equal(t.all(".ref-slot").length, 2);
    assert.equal(t.q("#quality").value, "480");
    assert.equal(t.q("#sage").checked, false);
    assert.ok(!t.q("#lora-model").textContent.includes("Krea2"));
    t.click('input[name="video-mode"][value="free"]');
    assert.equal(t.all(".ref-slot").length, 4);
    t.click('[data-action="picker"][data-slot="3"]');
    t.click('[data-action="pick-photo"][data-index="2"]');
    assert.ok(t.all(".ref-preview img").length === 1);
    t.click('[data-action="assistant"]');
    assert.match(t.q(".observations").textContent, /Фото 4/);
    t.click('[data-action="close"]');
    t.click('input[name="video-mode"][value="exact"]');
    t.click('[data-action="assistant"]');
    assert.equal(
      t.q(".observations"),
      null,
      "inactive extra references not described",
    );
    t.click('[data-action="close"]');
    t.click("#rife");
    assert.equal(t.q('[data-options="rife"]').hidden, false);
    assert.equal(t.q('[data-options="rtx"]').hidden, true);
    t.change("#quality", "1440");
    t.click("#rtx");
    assert.equal(t.q("#rtx-scale").value, "2");
    assert.equal(t.q("#rtx").disabled, false);
  } finally {
    t.close();
  }
});

test("library search, empty state and reuse do not require downloading", async () => {
  const t = setup();
  try {
    await t.page("library");
    t.input("#library-search", "not-found");
    assert.ok(t.q(".empty"));
    t.click('[data-action="reset-filter"]');
    assert.equal(t.all(".media-item").length, 4);
    t.click('[data-action="view-photo"][data-index="1"]');
    t.click('#dialog [data-action="animate"]');
    await new Promise((resolve) => setTimeout(resolve, 15));
    assert.ok(t.q(".ref-preview img").src.endsWith("landscape.jpg"));
    assert.equal(
      t.q('[data-kind="video"]').getAttribute("aria-selected"),
      "true",
    );
  } finally {
    t.close();
  }
});

test("dataset edits stay per frame, trigger is normalized once", async () => {
  const t = setup();
  try {
    await t.page("training");
    t.input("#caption", "nd_light, frame one");
    t.click('[data-action="caption-next"]');
    assert.ok(!t.q("#caption").value.includes("frame one"));
    t.click('[data-action="caption-prev"]');
    assert.equal(t.q("#caption").value, "nd_light, frame one");
    t.click('[data-action="caption-assistant"]');
    t.input("#caption-draft", "nd_light, nd_light, blue side light");
    t.click('[data-action="apply-caption"]');
    assert.equal(t.q("#caption").value, "nd_light, blue side light");
  } finally {
    t.close();
  }
});

test("admin quotas and user state edit independently in demo table", async () => {
  const t = setup();
  try {
    await t.page("users");
    t.click('[data-action="edit-user"][data-index="2"]');
    t.input("#access-photos", "45");
    t.input("#access-videos", "7");
    t.change("#access-quality", "1080");
    t.change("#access-priority", "Высокий");
    t.click('[data-action="save-user"]');
    const row = t.all("tbody tr")[2].textContent;
    assert.match(row, /45 фото · 7 видео/);
    assert.match(row, /1080p/);
    assert.match(row, /Высокий/);
    t.click('[data-action="invite"]');
    t.change("#invite-template", "Только изображения");
    assert.equal(t.q("#access-videos").value, "0");
    assert.ok(t.q("#invite-link-expiry") && t.q("#invite-account-expiry"));
  } finally {
    t.close();
  }
});

test("queue is honest: waiting job has no animated progress and cancel confirms", async () => {
  const t = setup();
  try {
    await t.page("jobs");
    assert.equal(
      t.all(".job-row")[1].querySelector("[role=progressbar]"),
      null,
    );
    t.click('[data-action="cancel-job"]');
    assert.ok(t.q("#dialog").open);
    t.click('[data-action="confirm-cancel"]');
    assert.match(t.all(".job-row")[1].textContent, /Отменено/);
  } finally {
    t.close();
  }
});

test("assets are local, complete and no server API is called", () => {
  for (const file of [
    "portrait.jpg",
    "portrait-2.jpg",
    "landscape.jpg",
    "interior.jpg",
    "lucide.js",
    "LUCIDE-LICENSE",
  ]) {
    assert.ok(fs.statSync(path.join(__dirname, "assets", file)).size > 500);
  }
  assert.ok(!/\b(fetch|XMLHttpRequest|WebSocket|EventSource)\s*\(/.test(code));
  assert.ok(!/<(?:script|img)[^>]+src=["']https?:/.test(html));
});
