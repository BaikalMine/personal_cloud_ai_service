const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const source = fs.readFileSync(path.join(__dirname, "../../static/auth-pages.js"), "utf8");

function setup({ disabled = false, referrer = "", historyLength = 1 } = {}) {
  function element(properties = {}) {
    return Object.assign({ attrs: {}, events: {}, hidden: true, textContent: "",
      setAttribute(name, value) { this.attrs[name] = value; }, removeAttribute(name) { delete this.attrs[name]; },
      addEventListener(name, fn) { this.events[name] = fn; }, replaceChildren(child) { this.child = child; }
    }, properties);
  }
  const password = element({ type: "password", value: "secret-test-password" });
  const button = element({ disabled });
  const toggle = element(); const progress = element(); const back = element();
  const form = element({ querySelector(selector) { return selector === '[data-password-toggle]' ? toggle : selector === 'input[name="password"]' ? password : selector === '[data-auth-progress]' ? progress : button; } });
  const documentEvents = [], windowEvents = {}, timers = [];
  let backCalls = 0;
  const context = { URL, WeakSet, Map, setTimeout(fn) { timers.push(fn); },
    document: { referrer, querySelectorAll: () => [form], querySelector: () => back, createElement: () => element(), addEventListener(name, fn, capture) { documentEvents.push({ name, fn, capture }); } },
    window: { location: { origin: "https://gateway.test" }, history: { length: historyLength, back() { backCalls++; } }, addEventListener(name, fn) { windowEvents[name] = fn; } }
  };
  vm.runInNewContext(source, context);
  const submit = (prevented = false, target = form) => {
    const event = { target, defaultPrevented: prevented, stopped: false, preventDefault() { this.defaultPrevented = true; }, stopImmediatePropagation() { this.stopped = true; } };
    for (const listener of documentEvents.filter(v => v.name === "submit").sort((a,b) => Number(b.capture || false)-Number(a.capture || false))) { listener.fn(event); if (event.stopped) break; }
    return event;
  };
  return { password, button, toggle, progress, form, back, submit, flush: () => timers.splice(0).forEach(fn => fn()), pageshow: persisted => windowEvents.pageshow({ persisted }), backCalls: () => backCalls };
}

test("auth controller toggles explicitly and remasks restored password without erasing input", () => {
  const f = setup(); assert.equal(f.toggle.hidden, false);
  f.toggle.events.click(); assert.equal(f.password.type, "text"); assert.equal(f.toggle.attrs["aria-pressed"], "true");
  assert.equal(f.toggle.child.attrs["data-lucide"], "eye-off");
  f.pageshow(false); assert.equal(f.password.type, "text");
  f.pageshow(true); assert.equal(f.password.type, "password"); assert.equal(f.toggle.attrs["aria-pressed"], "false");
  assert.equal(f.password.value, "secret-test-password");
});
test("auth native submit guards uncancelled submission and resets on bfcache", () => {
  const f = setup(); f.submit(true); assert.equal(f.form.attrs["aria-busy"], undefined);
  assert.equal(f.submit().defaultPrevented, false); assert.equal(f.form.attrs["aria-busy"], "true");
  assert.equal(f.button.disabled, false); f.flush(); assert.equal(f.button.disabled, true);
  assert.equal(f.submit().defaultPrevented, true); assert.ok(f.progress.textContent);
  f.pageshow(true); assert.equal(f.form.attrs["aria-busy"], undefined); assert.equal(f.button.disabled, false); assert.equal(f.progress.textContent, "");
  assert.equal(f.submit().defaultPrevented, false);
});
test("auth restore preserves server-disabled registration", () => {
  const f = setup({ disabled: true }); f.pageshow(true); assert.equal(f.button.disabled, true);
});
test("auth controller does not intercept unrelated forms", () => {
  const f = setup(); assert.equal(f.submit(false, {}).defaultPrevented, false); assert.equal(f.form.attrs["aria-busy"], undefined);
});
test("recovery back is offered only for usable same-origin history", () => {
  for (const options of [{}, { referrer: "https://outside.test/path", historyLength: 2 }, { referrer: "invalid", historyLength: 2 }, { referrer: "https://gateway.test/login", historyLength: 1 }]) {
    assert.equal(setup(options).back.hidden, true);
  }
  const f = setup({ referrer: "https://gateway.test/login", historyLength: 2 });
  assert.equal(f.back.hidden, false); f.back.events.click(); assert.equal(f.backCalls(), 1);
});
test("auth controller has no credential persistence or automatic network requests", () => {
  for (const forbidden of [/localStorage/, /sessionStorage/, /indexedDB/, /fetch\(/, /XMLHttpRequest/, /\.submit\(/, /requestSubmit\(/]) assert.doesNotMatch(source, forbidden);
});
